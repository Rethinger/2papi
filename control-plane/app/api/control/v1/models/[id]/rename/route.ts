import { z } from 'zod';
import { problem, ok, ApiError } from '@/lib/api';
import { tx } from '@/lib/db';
import { renameModelAlias } from '@/lib/codex/operations';

export const dynamic = 'force-dynamic';
type Ctx = { params: Promise<{ id: string }> };
const Body = z.object({ alias: z.string() });

export async function POST(req: Request, ctx: Ctx) {
  try {
    const { id } = await ctx.params;
    if (!id) throw new ApiError(400, 'missing_id', 'Model id is required');
    const body = Body.parse(await req.json());
    return ok(await tx(client => renameModelAlias(client, id, body.alias)));
  } catch (e) { return problem(e); }
}
