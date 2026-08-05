import { NextResponse } from 'next/server';
import { pool } from '@/lib/db';
import { ApiError, problem, requireInternal } from '@/lib/api';
import { env } from '@/lib/env';
import { runtimeSnapshotFromPublishedRow } from '@/lib/snapshots';

export const dynamic = 'force-dynamic';

export async function GET(req: Request) {
  try {
    requireInternal(req, env.INTERNAL_SERVICE_TOKEN);
    const client = await pool.connect();
    try {
      const row = await runtimeSnapshotFromPublishedRow(client);
      if (!row) throw new ApiError(404, 'not_found', 'Snapshot not found');
      return NextResponse.json(row);
    } finally { client.release(); }
  } catch (error) {
    return problem(error);
  }
}
