import { NextResponse } from 'next/server';
import { pool } from '@/lib/db';
import { ApiError, problem, readJsonBounded, requireInternal } from '@/lib/api';
import { env } from '@/lib/env';
import { persistGatewayAck } from '@/lib/control';

export const dynamic = 'force-dynamic';

export async function POST(req: Request) {
  try {
    requireInternal(req, env.INTERNAL_SERVICE_TOKEN);
    const body = await readJsonBounded<{
      gateway_id?: string;
      version?: number;
      config_version?: number;
      checksum?: string;
      success?: boolean;
      status?: 'adopted' | 'rejected';
      error?: string;
      schema_version?: number;
      config_checksum?: string;
      credential_digest?: string;
      runtime_checksum?: string;
      envelope_version?: number;
    }>(req, 64 * 1024);
    const version = body.config_version ?? body.version;
    const status = body.status ?? (typeof body.success === 'boolean' ? (body.success ? 'adopted' : 'rejected') : undefined);
    const checksum = body.runtime_checksum ?? body.checksum;
    if (!body.gateway_id || !version || !checksum || !status) {
      throw new ApiError(400, 'validation_failed', 'gateway_id, version, checksum and success are required');
    }
    const v2 = (body.envelope_version ?? 1) >= 2;
    if (v2 && (!body.schema_version || !body.config_checksum || !body.credential_digest || !body.runtime_checksum)) {
      throw new ApiError(400, 'validation_failed', 'v2 acknowledgement identity is incomplete');
    }
    const row = await persistGatewayAck(pool as any, {
      gateway_id: body.gateway_id,
      version,
      checksum,
      status,
      error: body.error,
      schema_version: body.schema_version,
      config_checksum: body.config_checksum ?? body.checksum,
      credential_digest: body.credential_digest,
      runtime_checksum: body.runtime_checksum,
      envelope_version: body.envelope_version,
    });
    return NextResponse.json(row, { status: 201 });
  } catch (error) {
    return problem(error);
  }
}
