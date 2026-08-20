import { ok, problem } from '@/lib/api';
import { refreshCodexQuota } from '@/lib/codex/quota';
import { pool } from '@/lib/db';

export const dynamic = 'force-dynamic';

export async function POST(_request: Request, context: { params: Promise<{ id: string }> }) {
  try {
    return ok(await refreshCodexQuota(pool, (await context.params).id));
  } catch (error) {
    return problem(error);
  }
}
