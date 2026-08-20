import { ok, problem, readJsonBounded } from '@/lib/api';
import { resetCodexQuota } from '@/lib/codex/quota';
import { pool } from '@/lib/db';

export const dynamic = 'force-dynamic';

export async function POST(request: Request, context: { params: Promise<{ id: string }> }) {
  try {
    const body = await readJsonBounded<{ confirmed?: unknown }>(request, 16 * 1024);
    const operation = await resetCodexQuota(
      pool,
      (await context.params).id,
      request.headers.get('idempotency-key') ?? '',
      body.confirmed === true,
    );
    return ok(operation, operation.status === 'pending' || operation.status === 'unknown' ? 202 : 200);
  } catch (error) {
    return problem(error);
  }
}
