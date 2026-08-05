import { codexDeviceStatusCore, codexRouteDeps } from '../../../../../../../../lib/codex/routes';

export async function GET(request: Request, context: { params: Promise<{ session: string }> }) {
  const { session } = await context.params;
  return codexDeviceStatusCore(request, session, codexRouteDeps());
}
