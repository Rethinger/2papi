import { NextResponse } from 'next/server';
import { pool } from '@/lib/db';
import { ApiError, problem, requireInternal } from '@/lib/api';
import { env } from '@/lib/env';

export const dynamic = 'force-dynamic';

export async function GET(req: Request) {
  try {
    requireInternal(req, env.INTERNAL_SERVICE_TOKEN);
    const q = await pool.query("SELECT version,checksum,snapshot FROM config_versions WHERE status='published' ORDER BY version DESC LIMIT 1");
    if (!q.rows[0]) throw new ApiError(404, 'not_found', 'Snapshot not found');
    return NextResponse.json({ ...q.rows[0], version: Number(q.rows[0].version) });
  } catch (error) {
    return problem(error);
  }
}
