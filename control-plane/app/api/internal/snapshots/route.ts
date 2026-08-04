import { pool } from '@/lib/db';
import { ok, problem, requireInternal, ApiError } from '@/lib/api';
import { env } from '@/lib/env';

export const dynamic = 'force-dynamic';

export async function GET(req: Request) {
  try {
    requireInternal(req, env.INTERNAL_SERVICE_TOKEN);
    const url = new URL(req.url);
    const version = url.searchParams.get('version');
    const q = version ? await pool.query('SELECT version,checksum,snapshot FROM config_versions WHERE version=$1', [version]) : await pool.query("SELECT version,checksum,snapshot FROM config_versions WHERE status='published' ORDER BY version DESC LIMIT 1");
    if (!q.rows[0]) throw new ApiError(404, 'not_found', 'Snapshot not found');
    return ok(q.rows[0]);
  } catch (e) { return problem(e); }
}

export async function POST(req: Request) {
  try {
    requireInternal(req, env.INTERNAL_SERVICE_TOKEN);
    const body = await req.json() as { gateway_id?: string; version?: number; checksum?: string; status?: 'adopted'|'rejected'; error?: string };
    if (!body.gateway_id || !body.version || !body.checksum || !body.status) throw new ApiError(400, 'validation_failed', 'gateway_id, version, checksum and status are required');
    const q = await pool.query('INSERT INTO gateway_config_acks (gateway_id,version,checksum,status,error) VALUES ($1,$2,$3,$4,$5) RETURNING *', [body.gateway_id, body.version, body.checksum, body.status, body.error ?? null]);
    return ok(q.rows[0], 201);
  } catch (e) { return problem(e); }
}
