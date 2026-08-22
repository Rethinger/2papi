import crypto from 'node:crypto';
import { z } from 'zod';
import { pool as defaultPool, type Queryable } from './db';
import { problem, ApiError } from './api';
import { env } from './env';
import { requireHosted } from './edition';
import { audit } from './control';

// Paddle Billing webhook (шаг 6 «Платежи»): transaction.completed →
// idempotent credit grant. Signature = HMAC-SHA256 over "<ts>:<rawBody>",
// header "Paddle-Signature: ts=…;h1=…". Retries are safe: the ledger's
// UNIQUE(source, external_id) makes double-credit impossible.

const MAX_AGE_SECONDS = 5 * 60;

export interface WebhookDeps {
  pool?: Queryable;
  secret?: string;
}

export function webhookDeps(): WebhookDeps {
  return { pool: defaultPool, secret: env.PADDLE_WEBHOOK_SECRET };
}

function verifySignature(rawBody: string, signatureHeader: string | null, secret: string): boolean {
  if (!signatureHeader) return false;
  const parts = Object.fromEntries(signatureHeader.split(';').map(kv => kv.split('=') as [string, string]));
  const ts = Number(parts.ts);
  const mac = parts.h1;
  if (!Number.isFinite(ts) || !mac) return false;
  if (Math.abs(Date.now() / 1000 - ts) > MAX_AGE_SECONDS) return false;
  const expected = crypto.createHmac('sha256', secret).update(`${ts}:${rawBody}`).digest('hex');
  const a = Buffer.from(mac);
  const b = Buffer.from(expected);
  return a.length === b.length && crypto.timingSafeEqual(a, b);
}

const EventSchema = z.object({
  event_type: z.string(),
  data: z.object({
    id: z.string().min(1),
    status: z.string().optional(),
    currency_code: z.string().optional(),
    // Paddle reports totals in minor units as strings ("1500" = $15.00).
    total: z.string(),
    custom_data: z.object({ team_id: z.string().uuid() }).passthrough().optional(),
  }),
});

export async function paddleWebhookCore(req: Request, deps: WebhookDeps = webhookDeps()) {
  try {
    requireHosted();
    const db = deps.pool!;
    const secret = deps.secret;
    if (!secret) throw new ApiError(503, 'webhook_not_configured', 'PADDLE_WEBHOOK_SECRET is not set');

    const rawBody = await req.text();
    if (!verifySignature(rawBody, req.headers.get('paddle-signature'), secret)) {
      throw new ApiError(401, 'invalid_signature', 'Webhook signature verification failed');
    }

    const event = EventSchema.parse(JSON.parse(rawBody));
    if (event.event_type !== 'transaction.completed') {
      return Response.json({ data: { ok: true, ignored: event.event_type } });
    }
    if (event.data.currency_code !== 'USD') {
      throw new ApiError(422, 'unsupported_currency', 'Only USD checkouts are supported in this phase');
    }
    if (event.data.status && event.data.status !== 'completed' && event.data.status !== 'paid') {
      throw new ApiError(422, 'transaction_not_paid', `Transaction status ${event.data.status} is not payable`);
    }
    if (!event.data.custom_data) {
      throw new ApiError(422, 'missing_team_reference', 'Checkout is missing custom_data.team_id');
    }

    const teamId = event.data.custom_data.team_id;
    const delta = Number(event.data.total) / 100;
    if (!(delta > 0)) throw new ApiError(422, 'invalid_amount', `Total ${event.data.total} is not a positive amount`);

    await db.query('BEGIN');
    try {
      const inserted = await db.query(
        `INSERT INTO credit_transactions (team_id, delta_usd, kind, source, external_id, note)
         VALUES ($1,$2,'topup','paddle',$3,$4)
         ON CONFLICT (source, external_id) WHERE external_id <> '' DO NOTHING RETURNING id`,
        [teamId, delta, event.data.id, `paddle transaction ${event.data.id}`],
      );
      if (inserted.rows[0]) {
        await db.query('UPDATE teams SET balance_usd = balance_usd + $2, updated_at=now() WHERE id=$1', [teamId, delta]);
        await audit(db, 'topup', 'team', teamId, { source: 'paddle', external_id: event.data.id, delta_usd: delta });
      }
      await db.query('COMMIT');
      return Response.json({ data: { ok: true, credited: Boolean(inserted.rows[0]), delta_usd: delta } });
    } catch (err) {
      await db.query('ROLLBACK');
      throw err;
    }
  } catch (e) {
    return problem(e);
  }
}
