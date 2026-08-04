import { pool } from '@/lib/db';
import { redis } from '@/lib/redis';
import { ok, problem } from '@/lib/api';
export const dynamic = 'force-dynamic';
export async function GET() {
  try {
    await pool.query('SELECT 1');
    if (redis.status === 'wait') await redis.connect();
    await redis.ping();
    return ok({ status: 'ready', postgres: 'ok', redis: 'ok' });
  } catch (e) { return problem(e); }
}
