import { claudeCallbackCore, claudeRouteDeps } from '../../lib/claude/routes';

export async function GET(request: Request) {
  return claudeCallbackCore(request, claudeRouteDeps());
}
