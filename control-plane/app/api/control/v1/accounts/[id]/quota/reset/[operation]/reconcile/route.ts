import { ok, problem } from '@/lib/api';
import { reconcileCodexQuotaReset } from '@/lib/codex/quota';
import { pool } from '@/lib/db';

export const dynamic = 'force-dynamic';

export async function POST(_request: Request, context: { params: Promise<{ id: string; operation: string }> }) {
  try {
    const { id, operation } = await context.params;
    return ok(await reconcileCodexQuotaReset(pool, id, operation));
  } catch (error) {
    return problem(error);
  }
}
