import { z } from 'zod';
import { problem, ok } from '@/lib/api';
import { tx } from '@/lib/db';
import { importSelection } from '@/lib/codex/operations';

export const dynamic = 'force-dynamic';

const Body = z.object({
  alias: z.string(),
  upstream_model: z.string().min(1),
  account_ids: z.array(z.string().uuid()).min(1),
  enabled: z.boolean().optional(),
});

export async function POST(req: Request) {
  try {
    const body = Body.parse(await req.json());
    return ok(await tx(client => importSelection(client, body)), 201);
  } catch (e) { return problem(e); }
}
