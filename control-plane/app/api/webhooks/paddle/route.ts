import { paddleWebhookCore, webhookDeps } from '../../../../lib/webhooks';

export const dynamic = 'force-dynamic';

export function POST(request: Request) {
  return paddleWebhookCore(request, webhookDeps());
}
