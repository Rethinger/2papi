import { codexReauthorizeCore, codexRouteDeps } from '../../../../../../../lib/codex/routes';

export async function POST(request: Request, context: { params: Promise<{ id: string }> }) {
  const { id } = await context.params;
  return codexReauthorizeCore(request, id, codexRouteDeps());
}
