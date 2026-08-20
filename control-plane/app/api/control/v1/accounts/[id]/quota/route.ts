import { ok, problem } from '@/lib/api';
import { getCodexQuota } from '@/lib/codex/quota';
import { pool } from '@/lib/db';

export const dynamic = 'force-dynamic';

export async function GET(_request: Request, context: { params: Promise<{ id: string }> }) {
  try {
    return ok(await getCodexQuota(pool, (await context.params).id));
  } catch (error) {
    return problem(error);
  }
}
