import { NextResponse } from 'next/server';
import { pool } from '@/lib/db';
import { ApiError, problem, requireInternal } from '@/lib/api';
import { env } from '@/lib/env';

export const dynamic = 'force-dynamic';

export async function POST(req: Request) {
  try {
    requireInternal(req, env.INTERNAL_SERVICE_TOKEN);
    const body = await req.json() as { gateway_id?: string; version?: number; checksum?: string; success?: boolean; error?: string };
    if (!body.gateway_id || !body.version || !body.checksum || typeof body.success !== 'boolean') {
      throw new ApiError(400, 'validation_failed', 'gateway_id, version, checksum and success are required');
    }
    const status = body.success ? 'adopted' : 'rejected';
    const q = await pool.query(
      'INSERT INTO gateway_config_acks (gateway_id,version,checksum,status,error) VALUES ($1,$2,$3,$4,$5) RETURNING *',
      [body.gateway_id, body.version, body.checksum, status, body.error ?? null],
    );
    return NextResponse.json(q.rows[0], { status: 201 });
  } catch (error) {
    return problem(error);
  }
}
