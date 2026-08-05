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
