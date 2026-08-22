import { z } from 'zod';
import type { Pool, PoolClient } from 'pg';

export const RequestAttemptSchema = z.object({
  account: z.string().min(1).max(200),
  adapter: z.string().min(1).max(100),
  alias: z.string().max(200).default(''),
  status: z.number().int().min(0).max(599),
  outcome: z.enum(['success', 'rate_limited', 'upstream_error', 'saturated', 'canceled', 'rejected', 'budget_exceeded', 'concurrency_limited']),
  latency_ms: z.number().int().nonnegative(),
  cooldown_ms: z.number().int().nonnegative().default(0),
}).strict();

export const RequestEventSchema = z.object({
  request_id: z.string().regex(/^[0-9a-f]{32}$/),
  occurred_at: z.string().datetime({ offset: true }).refine(
    value => Date.parse(value) <= Date.now() + 5 * 60 * 1000,
    'occurred_at cannot be more than five minutes in the future',
  ),
  endpoint: z.enum(['/v1/chat/completions', '/v1/responses']),
  public_model: z.string().min(1).max(300),
  upstream_model: z.string().max(300).default(''),
  virtual_key: z.string().max(200).default(''),
  virtual_key_id: z.string().uuid().or(z.literal('')).default(''),
  cost_usd: z.number().nonnegative().default(0),
  streaming: z.boolean().default(false),
  config_version: z.number().int().nonnegative().default(0),
  final_status: z.number().int().min(0).max(599),
  success: z.boolean(),
  total_latency_ms: z.number().int().nonnegative(),
  input_tokens: z.number().int().nonnegative().default(0),
  output_tokens: z.number().int().nonnegative().default(0),
  total_tokens: z.number().int().nonnegative().default(0),
  attempts: z.array(RequestAttemptSchema).max(32),
}).strict().superRefine((event, ctx) => {
  if (event.success !== (event.final_status >= 200 && event.final_status < 300)) {
    ctx.addIssue({ code: z.ZodIssueCode.custom, path: ['success'], message: 'success must match final_status' });
  }
  if (event.total_tokens !== event.input_tokens + event.output_tokens) {
    ctx.addIssue({ code: z.ZodIssueCode.custom, path: ['total_tokens'], message: 'total_tokens must equal input_tokens plus output_tokens' });
  }
});

export const RequestEventBatchSchema = z.object({
  gateway_id: z.string().regex(/^[A-Za-z0-9][A-Za-z0-9._-]{0,199}$/),
  events: z.array(RequestEventSchema).min(1).max(10),
}).strict();

export type RequestAttempt = z.infer<typeof RequestAttemptSchema>;
export type RequestEvent = z.infer<typeof RequestEventSchema>;
export type StoredRequestEvent = RequestEvent & { id: string; gateway_id: string; received_at: string };
export type RequestMetrics = {
  requests: number;
  successful: number;
  fallbacks: number;
  success_rate: number;
  p95_latency_ms: number;
  total_tokens: number;
};

type Database = Pool | PoolClient;

function isPool(database: Database): database is Pool {
  return 'connect' in database && typeof database.connect === 'function';
}

export async function storeRequestEvents(database: Database, gatewayId: string, events: RequestEvent[]) {
  const client = isPool(database) ? await database.connect() : database;
  try {
    await client.query('BEGIN');
    await client.query(`DELETE FROM request_events
      WHERE received_at < now() - interval '30 days'`);
    let inserted = 0;
    const insertedIds: string[] = [];
    for (const event of events) {
      const result = await client.query<{ id: string }>(`INSERT INTO request_events
        (gateway_id,request_id,occurred_at,endpoint,public_model,upstream_model,virtual_key,virtual_key_id,streaming,config_version,final_status,success,total_latency_ms,input_tokens,output_tokens,total_tokens,cost_usd)
        VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)
        ON CONFLICT (gateway_id,request_id) DO NOTHING RETURNING id`, [
        gatewayId, event.request_id, event.occurred_at, event.endpoint, event.public_model,
        event.upstream_model, event.virtual_key, event.virtual_key_id || null, event.streaming,
        event.config_version, event.final_status, event.success, event.total_latency_ms,
        event.input_tokens, event.output_tokens, event.total_tokens, event.cost_usd,
      ]);
      const requestEventId = result.rows[0]?.id;
      if (!requestEventId) continue;
      inserted += 1;
      insertedIds.push(requestEventId);
      for (let position = 0; position < event.attempts.length; position += 1) {
        const attempt = event.attempts[position];
        await client.query(`INSERT INTO request_event_attempts
          (request_event_id,position,account,adapter,alias,status,outcome,latency_ms,cooldown_ms)
          VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`, [
          requestEventId, position, attempt.account, attempt.adapter, attempt.alias || '',
          attempt.status, attempt.outcome, attempt.latency_ms, attempt.cooldown_ms,
        ]);
      }
    }
    if (insertedIds.length > 0) {
      await client.query(`INSERT INTO key_spend_daily (virtual_key_id, day, cost_usd, tokens_in, tokens_out, requests)
        SELECT virtual_key_id, (occurred_at AT TIME ZONE 'UTC')::date, sum(cost_usd), sum(input_tokens), sum(output_tokens), count(*)
        FROM request_events
        WHERE id = ANY($1::bigint[]) AND virtual_key_id IS NOT NULL AND success
        GROUP BY virtual_key_id, (occurred_at AT TIME ZONE 'UTC')::date
        ON CONFLICT (virtual_key_id, day) DO UPDATE SET
          cost_usd = key_spend_daily.cost_usd + EXCLUDED.cost_usd,
          tokens_in = key_spend_daily.tokens_in + EXCLUDED.tokens_in,
          tokens_out = key_spend_daily.tokens_out + EXCLUDED.tokens_out,
          requests = key_spend_daily.requests + EXCLUDED.requests`,
      [insertedIds]);
      // Шаг 6 «Платежи»: decrement prepaid team balances by the spend just
      // recorded. Only teams that track a balance (> 0) are touched; the
      // gateway enforces the cap from its snapshot, this keeps the ledger
      // side honest between reconciles.
      await client.query(`
        UPDATE teams t SET balance_usd = t.balance_usd - agg.cost
        FROM (
          SELECT vk.team_id, SUM(re.cost_usd) AS cost
          FROM request_events re
          JOIN virtual_keys vk ON vk.id = re.virtual_key_id
          WHERE re.id = ANY($1::bigint[]) AND re.success AND vk.team_id IS NOT NULL
          GROUP BY vk.team_id
        ) agg
        WHERE t.id = agg.team_id AND t.balance_usd > 0`,
      [insertedIds]);
    }
    await client.query('COMMIT');
    return { inserted };
  } catch (error) {
    await client.query('ROLLBACK');
    throw error;
  } finally {
    if (isPool(database)) client.release();
  }
}

export async function listRequestEvents(database: Database, options: { limit?: number } = {}): Promise<StoredRequestEvent[]> {
  const limit = Math.min(Math.max(options.limit ?? 100, 1), 200);
  const result = await database.query(`SELECT e.*,
    COALESCE((SELECT jsonb_agg(jsonb_build_object(
      'account',a.account,'adapter',a.adapter,'alias',a.alias,'status',a.status,'outcome',a.outcome,
      'latency_ms',a.latency_ms,'cooldown_ms',a.cooldown_ms
    ) ORDER BY a.position) FROM request_event_attempts a WHERE a.request_event_id=e.id),'[]'::jsonb) attempts
    FROM request_events e ORDER BY e.occurred_at DESC,e.id DESC LIMIT $1`, [limit]);
  return result.rows.map(row => ({
    ...row,
    id: String(row.id),
    config_version: Number(row.config_version),
    final_status: Number(row.final_status),
    total_latency_ms: Number(row.total_latency_ms),
    input_tokens: Number(row.input_tokens),
    output_tokens: Number(row.output_tokens),
    total_tokens: Number(row.total_tokens),
    attempts: RequestAttemptSchema.array().parse(row.attempts),
  })) as StoredRequestEvent[];
}

export async function accountUsageSince(database: Database, accountName: string, since: Date): Promise<{ tokens: number; requests: number }> {
  const result = await database.query(
    `SELECT COALESCE(sum(e.input_tokens + e.output_tokens), 0)::bigint tokens,
            count(DISTINCT e.id)::bigint requests
     FROM request_events e
     JOIN request_event_attempts a ON a.request_event_id = e.id AND a.position = 0
     WHERE a.account = $1 AND e.occurred_at >= $2`,
    [accountName, since],
  );
  const row = result.rows[0] ?? {};
  return { tokens: Number(row.tokens ?? 0), requests: Number(row.requests ?? 0) };
}

export async function requestMetrics(database: Database, options: { since?: string } = {}): Promise<RequestMetrics> {
  const since = options.since ?? new Date(Date.now() - 24 * 60 * 60 * 1000).toISOString();
  const result = await database.query(`SELECT
    count(*)::int requests,
    count(*) FILTER (WHERE success)::int successful,
    count(*) FILTER (WHERE (SELECT count(*) FROM request_event_attempts a WHERE a.request_event_id=e.id) > 1)::int fallbacks,
    COALESCE(percentile_cont(0.95) WITHIN GROUP (ORDER BY total_latency_ms),0)::float8 p95_latency_ms,
    COALESCE(sum(total_tokens),0)::bigint total_tokens
    FROM request_events e WHERE occurred_at >= $1`, [since]);
  const row = result.rows[0];
  const requests = Number(row.requests);
  const successful = Number(row.successful);
  return {
    requests,
    successful,
    fallbacks: Number(row.fallbacks),
    success_rate: requests === 0 ? 0 : successful / requests,
    p95_latency_ms: Number(row.p95_latency_ms),
    total_tokens: Number(row.total_tokens),
  };
}
