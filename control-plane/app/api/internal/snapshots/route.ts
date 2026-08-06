import { pool } from '@/lib/db';
import { ok, problem, readJsonBounded, requireInternal, ApiError } from '@/lib/api';
import { env } from '@/lib/env';
import { runtimeSnapshotFromPublishedRow } from '@/lib/snapshots';

export const dynamic = 'force-dynamic';

export async function GET(req: Request) {
  try {
    requireInternal(req, env.INTERNAL_SERVICE_TOKEN);
    const url = new URL(req.url);
    const version = url.searchParams.get('version');
    const client = await pool.connect();
    try {
      const row = await runtimeSnapshotFromPublishedRow(client, version ?? undefined);
      if (!row) throw new ApiError(404, 'not_found', 'Snapshot not found');
      return ok(row);
    } finally { client.release(); }
  } catch (e) { return problem(e); }
}

export async function POST(req: Request) {
  try {
    requireInternal(req, env.INTERNAL_SERVICE_TOKEN);
    const body = await readJsonBounded<{ gateway_id?: string; version?: number; checksum?: string; status?: 'adopted'|'rejected'; error?: string }>(req, 64 * 1024);
    if (!body.gateway_id || !body.version || !body.checksum || !body.status) throw new ApiError(400, 'validation_failed', 'gateway_id, version, checksum and status are required');
    const q = await pool.query('INSERT INTO gateway_config_acks (gateway_id,version,checksum,status,error) VALUES ($1,$2,$3,$4,$5) RETURNING *', [body.gateway_id, body.version, body.checksum, body.status, body.error ?? null]);
    return ok(q.rows[0], 201);
  } catch (e) { return problem(e); }
}
