import Redis from 'ioredis';
import { env } from './env';

const useMemoryRedis = env.CODEX_TEST_MODE && env.REDIS_URL === 'redis://memory';
const memory = new Map<string, { value: string; expires: number }>();
const globalForRedis = globalThis as unknown as { redis?: Redis };
export const redis = useMemoryRedis ? undefined as unknown as Redis : (globalForRedis.redis ?? new Redis(env.REDIS_URL, { lazyConnect: true, maxRetriesPerRequest: 2 }));
if (!useMemoryRedis && process.env.NODE_ENV !== 'production') globalForRedis.redis = redis;

async function ensureRedis() {
  if (useMemoryRedis) return;
  if (redis.status === 'wait') await redis.connect();
}

export async function publishConfigVersion(version: number, checksum: string) {
  await ensureRedis();
  if (useMemoryRedis) { memory.set('papi:config:latest', { value: JSON.stringify({ version, checksum }), expires: Infinity }); return; }
  await redis.publish('papi:config:versions', JSON.stringify({ version, checksum, published_at: new Date().toISOString() }));
  await redis.set('papi:config:latest', JSON.stringify({ version, checksum }));
}

export async function setJsonWithTtl(key: string, value: unknown, ttlSeconds: number) {
  await ensureRedis();
  if (useMemoryRedis) { memory.set(key, { value: JSON.stringify(value), expires: Date.now() + ttlSeconds * 1000 }); return; }
  await redis.set(key, JSON.stringify(value), 'EX', ttlSeconds);
}

export async function setJsonUntilMs(key: string, value: unknown, expiresAtMs: number) {
  await ensureRedis();
  if (useMemoryRedis) { memory.set(key, { value: JSON.stringify(value), expires: expiresAtMs }); return; }
  await redis.psetex(key, Math.max(1, expiresAtMs - Date.now()), JSON.stringify(value));
}

export async function consumeJsonOnce<T>(key: string): Promise<T | null> {
  await ensureRedis();
  if (useMemoryRedis) {
    const hit = memory.get(key);
    memory.delete(key);
    if (!hit || hit.expires <= Date.now()) return null;
    return JSON.parse(hit.value) as T;
  }
  const raw = await redis.eval("local v=redis.call('GET', KEYS[1]); if v then redis.call('DEL', KEYS[1]); end; return v", 1, key) as string | null;
  return raw ? JSON.parse(raw) as T : null;
}

export async function consumeJsonWhenDue<T extends { next_poll_at_ms?: number }>(key: string, nowMs = Date.now()): Promise<{ status: 'missing'|'too_soon'|'due'; value?: T; ttlSeconds?: number }> {
  await ensureRedis();
  if (useMemoryRedis) {
    const hit = memory.get(key);
    if (!hit || hit.expires <= nowMs) { memory.delete(key); return { status: 'missing' }; }
    const value = JSON.parse(hit.value) as T;
    const ttlSeconds = Math.max(1, Math.ceil((hit.expires - nowMs) / 1000));
    if (typeof value.next_poll_at_ms === 'number' && value.next_poll_at_ms > nowMs) return { status: 'too_soon', value, ttlSeconds };
    memory.delete(key);
    return { status: 'due', value, ttlSeconds };
  }
  const raw = await redis.eval("local v=redis.call('GET', KEYS[1]); if not v then return {0}; end; local ttl=redis.call('PTTL', KEYS[1]); if ttl <= 0 then return {0}; end; local obj=cjson.decode(v); if obj['next_poll_at_ms'] and obj['next_poll_at_ms'] > tonumber(ARGV[1]) then return {1, v, ttl}; end; redis.call('DEL', KEYS[1]); return {2, v, ttl};", 1, key, String(nowMs)) as [number, string?, number?];
  if (raw[0] === 0) return { status: 'missing' };
  return { status: raw[0] === 1 ? 'too_soon' : 'due', value: JSON.parse(raw[1]!) as T, ttlSeconds: Math.max(1, Math.ceil((raw[2] ?? 1000) / 1000)) };
}

export async function leaseJsonWhenDue<T extends { next_poll_at_ms?: number; lease_until_ms?: number }>(key: string, leaseMs: number, nowMs = Date.now()): Promise<{ status: 'missing'|'too_soon'|'leased'; value?: T; ttlMs?: number }> {
  await ensureRedis();
  if (useMemoryRedis) {
    const hit = memory.get(key);
    if (!hit || hit.expires <= nowMs) { memory.delete(key); return { status: 'missing' }; }
    const value = JSON.parse(hit.value) as T;
    if ((typeof value.next_poll_at_ms === 'number' && value.next_poll_at_ms > nowMs) || (typeof value.lease_until_ms === 'number' && value.lease_until_ms > nowMs)) return { status: 'too_soon', value, ttlMs: hit.expires - nowMs };
    const leased = { ...value, lease_until_ms: nowMs + leaseMs };
    memory.set(key, { value: JSON.stringify(leased), expires: hit.expires });
    return { status: 'leased', value: leased as T, ttlMs: hit.expires - nowMs };
  }
  const raw = await redis.eval("local v=redis.call('GET', KEYS[1]); if not v then return {0}; end; local ttl=redis.call('PTTL', KEYS[1]); if ttl <= 0 then return {0}; end; local obj=cjson.decode(v); local now=tonumber(ARGV[1]); if (obj['next_poll_at_ms'] and obj['next_poll_at_ms'] > now) or (obj['lease_until_ms'] and obj['lease_until_ms'] > now) then return {1, v, ttl}; end; obj['lease_until_ms']=now+tonumber(ARGV[2]); local nv=cjson.encode(obj); redis.call('PSETEX', KEYS[1], ttl, nv); return {2, nv, ttl};", 1, key, String(nowMs), String(leaseMs)) as [number, string?, number?];
  if (raw[0] === 0) return { status: 'missing' };
  return { status: raw[0] === 1 ? 'too_soon' : 'leased', value: JSON.parse(raw[1]!) as T, ttlMs: raw[2] ?? 0 };
}

export async function updateLeasedJson<T extends { lease_until_ms?: number }>(key: string, value: T, ttlMs: number | undefined, mode: 'set'|'delete' = 'set') {
  await ensureRedis();
  if (useMemoryRedis) {
    if (mode === 'delete') { memory.delete(key); return; }
    memory.set(key, { value: JSON.stringify({ ...value, lease_until_ms: undefined }), expires: Date.now() + Math.max(1, ttlMs ?? 1000) });
    return;
  }
  if (mode === 'delete') await redis.del(key);
  else await redis.psetex(key, Math.max(1, ttlMs ?? 1000), JSON.stringify({ ...value, lease_until_ms: undefined }));
}
