import { z } from 'zod';
import { problem, ok } from '@/lib/api';
import { pool } from '@/lib/db';
import { discoverModelsForScope } from '@/lib/codex/operations';

export const dynamic = 'force-dynamic';

const Body = z.discriminatedUnion('scope', [
  z.object({ scope: z.literal('all') }),
  z.object({ scope: z.literal('provider_id'), provider_id: z.string().uuid() }),
  z.object({ scope: z.literal('account_id'), account_id: z.string().uuid() }),
]);

export async function POST(req: Request) {
  try {
    const body = Body.parse(await req.json());
    return ok(await discoverModelsForScope(pool, body));
  } catch (e) { return problem(e); }
}
