import { problem, ok } from '@/lib/api';
import { pool } from '@/lib/db';
import { groupedDiscoveredModels } from '@/lib/codex/operations';

export const dynamic = 'force-dynamic';

export async function GET() {
  try { return ok(await groupedDiscoveredModels(pool as any)); } catch (e) { return problem(e); }
}
