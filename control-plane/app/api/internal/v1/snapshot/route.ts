import { pool } from '@/lib/db';
import { ApiError, problem, requireInternal } from '@/lib/api';
import { env } from '@/lib/env';
import { snapshotEnvelope } from '@/lib/snapshot-envelope';
import { canonicalJson } from '@/lib/canonical-json';

export const dynamic = 'force-dynamic';

export async function GET(req: Request) {
  try {
    requireInternal(req, env.INTERNAL_SERVICE_TOKEN);
    const client = await pool.connect();
    try {
      const row = await snapshotEnvelope(client, req);
      if (!row) throw new ApiError(404, 'not_found', 'Snapshot not found');
      return new Response(canonicalJson(row), { headers: { 'content-type': 'application/json; charset=utf-8' } });
    } finally {
      client.release();
    }
  } catch (error) {
    return problem(error);
  }
}
