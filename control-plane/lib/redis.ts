import Redis from 'ioredis';
import { env } from './env';

const globalForRedis = globalThis as unknown as { redis?: Redis };
export const redis = globalForRedis.redis ?? new Redis(env.REDIS_URL, { lazyConnect: true, maxRetriesPerRequest: 2 });
if (process.env.NODE_ENV !== 'production') globalForRedis.redis = redis;

export async function publishConfigVersion(version: number, checksum: string) {
  if (redis.status === 'wait') await redis.connect();
  await redis.publish('papi:config:versions', JSON.stringify({ version, checksum, published_at: new Date().toISOString() }));
  await redis.set('papi:config:latest', JSON.stringify({ version, checksum }));
}
