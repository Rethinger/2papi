import { NextResponse } from 'next/server';
import { pool } from '@/lib/db';
import { problem, readJsonBounded, requireGatewayIdentity, requireInternal } from '@/lib/api';
import { env } from '@/lib/env';
import { RequestEventBatchSchema, storeRequestEvents } from '@/lib/request-events';

export const dynamic = 'force-dynamic';

export async function POST(req: Request) {
  try {
    requireInternal(req, env.INTERNAL_SERVICE_TOKEN);
    const raw = await readJsonBounded<unknown>(req, 256 * 1024);
    const batch = RequestEventBatchSchema.parse(raw);
    const gatewayId = requireGatewayIdentity(req, batch.gateway_id);
    await storeRequestEvents(pool, gatewayId, batch.events);
    return new NextResponse(null, { status: 204 });
  } catch (error) {
    return problem(error);
  }
}
