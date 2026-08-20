import { claudeOAuthStartCore, claudeRouteDeps } from '../../../../../../../lib/claude/routes';

export async function POST(request: Request) {
  return claudeOAuthStartCore(request, claudeRouteDeps());
}
