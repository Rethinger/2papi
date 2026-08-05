import { codexCallbackCore, codexRouteDeps } from '../../../lib/codex/routes';

export async function GET(request: Request) {
  return codexCallbackCore(request, codexRouteDeps());
}
