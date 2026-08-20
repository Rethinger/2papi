import { ok, problem, readJsonBounded } from '@/lib/api';
import { resolveCodexQuotaReset } from '@/lib/codex/quota';
import { pool } from '@/lib/db';

export const dynamic = 'force-dynamic';

export async function POST(request: Request, context: { params: Promise<{ id: string; operation: string }> }) {
  try {
    const { id, operation } = await context.params;
    const body = await readJsonBounded<{ resolution?: unknown; note?: unknown }>(request, 16 * 1024);
    const resolution = body.resolution === 'succeeded' || body.resolution === 'failed' ? body.resolution : '';
    return ok(await resolveCodexQuotaReset(pool, id, operation, resolution as 'succeeded' | 'failed', typeof body.note === 'string' ? body.note : ''));
  } catch (error) {
    return problem(error);
  }
}
