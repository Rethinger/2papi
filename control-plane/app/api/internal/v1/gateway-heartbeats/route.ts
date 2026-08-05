import { NextResponse } from 'next/server';
import { pool } from '@/lib/db';
import { ApiError, problem, readJsonBounded, requireInternal } from '@/lib/api';
import { env } from '@/lib/env';
import { upsertGatewayHeartbeat } from '@/lib/control';

export const dynamic = 'force-dynamic';

export async function POST(req: Request) {
  try {
    requireInternal(req, env.INTERNAL_SERVICE_TOKEN);
    const body = await readJsonBounded<{ gateway_id?: string; supported_schemas?: number[]; envelope_version?: number }>(req, 16 * 1024);
    if (!body.gateway_id || !Array.isArray(body.supported_schemas) || !body.supported_schemas.every(Number.isInteger) || !Number.isInteger(body.envelope_version)) {
      throw new ApiError(400, 'validation_failed', 'gateway_id, supported_schemas and envelope_version are required');
    }
    await upsertGatewayHeartbeat(pool as any, { gateway_id: body.gateway_id, supported_schemas: body.supported_schemas, envelope_version: body.envelope_version! });
    return NextResponse.json({ gateway_id: body.gateway_id, supported_schemas: body.supported_schemas, envelope_version: body.envelope_version }, { status: 201 });
  } catch (error) { return problem(error); }
}
