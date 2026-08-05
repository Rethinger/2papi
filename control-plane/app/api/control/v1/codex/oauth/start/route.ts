import { codexOAuthStartCore, codexRouteDeps } from '../../../../../../../lib/codex/routes';

export async function POST(request: Request) {
  return codexOAuthStartCore(request, codexRouteDeps());
}
