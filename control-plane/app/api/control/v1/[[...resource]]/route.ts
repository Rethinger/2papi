import crypto from 'node:crypto';
import { pool, tx } from '@/lib/db';
import { ok, problem, ApiError } from '@/lib/api';
import { publishConfigVersion } from '@/lib/redis';
import {
  AccountPatchSchema,
  AccountSchema,
  ModelPatchSchema,
  ModelSchema,
  ProviderPatchSchema,
  ProviderSchema,
  RoutingSchema,
  TeamPatchSchema,
  TeamSchema,
  VirtualKeyPatchSchema,
  VirtualKeySchema,
  WebhookSchema,
  audit,
  insertSecret,
  publishLatest,
  storeDraft,
} from '@/lib/control';
import { sha256Canonical } from '@/lib/canonical-json';
import { deleteAccountResource, deleteProviderResource } from '@/lib/resource-deletion';
import { deleteModelRoute, updateProviderModelStrategy } from '@/lib/model-routes';
import { mergeModelMetadata, normalizeModelMetadata, planIncompatibility } from '@/lib/model-metadata';
import { listRequestEvents, requestMetrics } from '@/lib/request-events';
import { summarizeQuota } from '@/lib/quota';
import { exportSnapshot, importSnapshot } from '@/lib/snapshot-transfer';
import { parseProxyList, normalizeProxy } from '@/lib/proxylib';

// saveProxyPool persists the global proxy pool (raw text, any format)
// and validates it with the same rules as the gateway parser.
async function saveProxyPool(body: unknown) {
  const raw = typeof (body as { raw?: unknown })?.raw === 'string' ? (body as { raw: string }).raw : '';
  const { entries, errors } = parseProxyList(raw);
  if (errors.length) {
    throw new ApiError(400, 'invalid_proxy_pool', errors.map(e => `line ${e.line}: ${e.text} — ${e.reason}`).join('; '));
  }
  const saved = await tx(async client => {
    await client.query(`INSERT INTO system_settings (key,value,updated_at) VALUES ('proxy_pool',$1,now()) ON CONFLICT (key) DO UPDATE SET value=EXCLUDED.value, updated_at=now()`, [JSON.stringify({ raw })]);
    await draftAfter(client, 'update', 'proxy_pool', 'global', { proxy_count: entries.length });
    return { raw, proxy_count: entries.length };
  });
  return saved;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === 'object' && !Array.isArray(value);
}

export const dynamic = 'force-dynamic';
type Ctx = { params: Promise<{ resource?: string[] }> };
const pathOf = async (ctx: Ctx) => (await ctx.params).resource ?? ['overview'];
const json = async (req: Request) => req.headers.get('content-length') === '0' ? {} : req.json();

async function draftAfter(client: any, action: string, typ: string, id?: string, payload?: unknown) { await audit(client, action, typ, id, payload); return storeDraft(client); }

export async function GET(req: Request, ctx: Ctx) {
  try {
    const p = await pathOf(ctx); const r = p[0];
    if (r === 'overview') {
      const [q, metrics] = await Promise.all([
        pool.query(`SELECT (SELECT count(*) FROM providers) providers,(SELECT count(*) FROM accounts) accounts,(SELECT count(*) FROM model_aliases) models,(SELECT count(*) FROM virtual_keys) virtual_keys,(SELECT max(version) FROM config_versions WHERE status='published') published_version`),
        requestMetrics(pool),
      ]);
      return ok({ ...q.rows[0], requests_24h: metrics.requests, success_rate_24h: metrics.success_rate, p95_latency_ms_24h: metrics.p95_latency_ms, tokens_24h: metrics.total_tokens });
    }
    if (r === 'request-events') {
      const requested = Number(new URL(req.url).searchParams.get('limit') ?? 100);
      return ok(await listRequestEvents(pool, { limit: Number.isFinite(requested) ? requested : 100 }));
    }
    if (r === 'request-metrics') return ok(await requestMetrics(pool));
    if (r === 'quota') return ok(await summarizeQuota(pool));
    if (r === 'providers') return ok((await pool.query('SELECT * FROM providers ORDER BY name')).rows);
    if (r === 'accounts') return ok((await pool.query(`SELECT a.*, sr.key_version, sr.rotated_at, sr.id secret_id, aps.last_error_code, aps.last_error_message, aps.last_operation, aps.updated_at last_probe_at
      FROM accounts a
      LEFT JOIN secret_records sr ON sr.id=a.secret_record_id
      LEFT JOIN account_provider_state aps ON aps.account_id=a.id
      ORDER BY a.name`)).rows.map(row => ({ ...row, secret_present: Boolean(row.secret_id) })));
    if (r === 'models') {
      const models = (await pool.query(`SELECT ma.*,p.name provider_name,p.slug provider_slug,p.adapter,
        mp.input_per_mtok, mp.output_per_mtok,
        COALESCE(json_agg(DISTINCT mam.account_id) FILTER (WHERE mam.account_id IS NOT NULL),'[]') accounts
        FROM model_aliases ma
        LEFT JOIN providers p ON p.id=ma.provider_id
        LEFT JOIN model_pricing mp ON mp.model_alias_id=ma.id
        LEFT JOIN model_account_mappings mam ON mam.model_alias_id=ma.id
        GROUP BY ma.id,p.name,p.slug,p.adapter,mp.input_per_mtok,mp.output_per_mtok ORDER BY ma.alias`)).rows;
      const discovered = (await pool.query(`SELECT dm.provider_id,dm.upstream_model,dm.account_id,dm.raw_metadata,dm.capabilities,dm.supported_in_api,dm.last_seen_at
        FROM discovered_models dm JOIN accounts a ON a.id=dm.account_id WHERE dm.available=true AND a.enabled=true ORDER BY a.priority,a.name`)).rows;
      const planRows: Array<{ id: string; plan_type: string | null }> = (await pool.query('SELECT id, plan_type FROM accounts')).rows;
      const planByAccount = new Map(planRows.map(row => [row.id, row.plan_type]));
      return ok(models.map(model => {
        const normalized = { ...model, input_per_mtok: Number(model.input_per_mtok ?? 0), output_per_mtok: Number(model.output_per_mtok ?? 0) };
        if (!model.provider_id) return { ...normalized, metadata: null, plan_incompatible: false, available_account_count: model.accounts.length, eligible_account_ids: model.accounts };
        const rows = discovered.filter(row => row.provider_id === model.provider_id && row.upstream_model === model.upstream_model);
        const metadata = mergeModelMetadata(rows.map(row => normalizeModelMetadata({ ...(row.raw_metadata ?? {}), capabilities: row.capabilities, last_seen_at: row.last_seen_at ? new Date(row.last_seen_at).toISOString() : null })));
        return { ...normalized, metadata, plan_incompatible: planIncompatibility(metadata, model.accounts ?? [], planByAccount), available_account_count: rows.length, eligible_account_ids: rows.map(row => row.account_id) };
      }));
    }
    if (r === 'gateway-acks') return ok((await pool.query('SELECT gateway_id,version,checksum,status,error,acknowledged_at FROM gateway_config_acks ORDER BY acknowledged_at DESC LIMIT 200')).rows.map(row => ({ ...row, version: Number(row.version) })));
    if (r === 'routing') {
      const row = (await pool.query('SELECT * FROM routing_settings WHERE id=true')).rows[0];
      if (!row) throw new ApiError(404, 'not_found', 'Routing settings not found');
      const optimizationRow = (await pool.query(`SELECT value FROM system_settings WHERE key='optimization'`)).rows[0];
      return ok({
        ...row,
        resilience: {
          cooldown: row.resilience?.cooldown ?? '30s',
          circuit_failures: Number(row.resilience?.circuit_failures ?? 3),
          circuit_reset: row.resilience?.circuit_reset ?? '1m',
          lockout_failures: Number(row.resilience?.lockout_failures ?? 10),
          lockout_duration: row.resilience?.lockout_duration ?? '15m',
        },
        optimization: { rtk_compression: Boolean(optimizationRow?.value?.rtk_compression), caveman: Boolean(optimizationRow?.value?.caveman), headroom: Boolean(optimizationRow?.value?.headroom), headroom_reserve: Number(optimizationRow?.value?.headroom_reserve) || 120000, headroom_keep: Number(optimizationRow?.value?.headroom_keep) || 8 },
      });
    }
    if (r === 'virtual-keys') return ok((await pool.query(`SELECT vk.id,vk.name,vk.key_prefix,vk.enabled,vk.models,vk.rpm,vk.tpm,vk.max_concurrency,vk.budget_usd,vk.team_id,t.name team_name,vk.created_at,vk.last_used_at,
        COALESCE(ksd.cost_usd,0) spend_today, COALESCE(ksd.tokens_in,0) tokens_today, COALESCE(ksd.requests,0) requests_today
        FROM virtual_keys vk
        LEFT JOIN teams t ON t.id=vk.team_id
        LEFT JOIN key_spend_daily ksd ON ksd.virtual_key_id=vk.id AND ksd.day=(now() AT TIME ZONE 'UTC')::date
        ORDER BY vk.name`)).rows.map(row => ({ ...row, budget_usd: Number(row.budget_usd), spend_today: Number(row.spend_today), tokens_today: Number(row.tokens_today), requests_today: Number(row.requests_today) })));
    if (r === 'teams') return ok((await pool.query(`SELECT t.*, count(vk.id)::int key_count,
        COALESCE(SUM(CASE WHEN ksd.day=(now() AT TIME ZONE 'UTC')::date THEN ksd.cost_usd ELSE 0 END),0) spend_today
      FROM teams t
      LEFT JOIN virtual_keys vk ON vk.team_id=t.id AND vk.enabled
      LEFT JOIN key_spend_daily ksd ON ksd.virtual_key_id=vk.id
      GROUP BY t.id ORDER BY t.name`)).rows.map(row => ({ ...row, budget_usd: Number(row.budget_usd), spend_today: Number(row.spend_today), share_usd: Number(row.key_count) > 0 && Number(row.budget_usd) > 0 ? Math.round(Number(row.budget_usd) / Number(row.key_count) * 1e6) / 1e6 : 0 })));
    if (r === 'settings') return ok((await pool.query('SELECT * FROM system_settings ORDER BY key')).rows);
    if (r === 'proxy-pool') {
      const row = (await pool.query(`SELECT value FROM system_settings WHERE key='proxy_pool'`)).rows[0];
      return ok({ raw: typeof row?.value?.raw === 'string' ? row.value.raw : '' });
    }
    if (r === 'export') return ok(await exportSnapshot(pool));
    if (r === 'request-trends') {
      const requested = Number(new URL(req.url).searchParams.get('days') ?? 14);
      const days = Math.min(Math.max(Number.isFinite(requested) ? requested : 14, 1), 90);
      const rows = (await pool.query(`SELECT (occurred_at AT TIME ZONE 'UTC')::date AS req_day,
        count(*)::int requests,
        sum(total_tokens)::bigint tokens,
        round(100.0 * count(*) FILTER (WHERE success) / NULLIF(count(*),0), 1) success_rate
        FROM request_events WHERE occurred_at > now() - ($1::int * interval '1 day')
        GROUP BY req_day ORDER BY req_day`, [days])).rows;
      return ok(rows.map((row: any) => ({ ...row, day: row.req_day, tokens: Number(row.tokens ?? 0) })));
    }
    if (r === 'webhook') {
      const row = (await pool.query(`SELECT value FROM system_settings WHERE key='webhook'`)).rows[0];
      return ok({ enabled: Boolean(row?.value?.enabled), url: typeof row?.value?.url === 'string' ? row.value.url : '', secret: typeof row?.value?.secret === 'string' ? row.value.secret : '' });
    }
    if (r === 'audit-events') return ok((await pool.query('SELECT * FROM audit_events ORDER BY id DESC LIMIT 200')).rows);
    if (r === 'config-versions') return ok((await pool.query('SELECT version,status,checksum,errors,published_at,created_at,source_version FROM config_versions ORDER BY version DESC LIMIT 100')).rows);
    throw new ApiError(404, 'not_found', `Unknown resource ${r}`);
  } catch (e) { return problem(e); }
}

export async function POST(req: Request, ctx: Ctx) {
  try {
    const p = await pathOf(ctx); const r = p[0]; const body = await json(req);
    if (r === 'providers') return ok(await tx(async c => { const v = ProviderSchema.parse(body); const q = await c.query('INSERT INTO providers (slug,name,adapter,base_url,enabled,metadata) VALUES ($1,$2,$3,$4,$5,$6) RETURNING *', [v.slug,v.name,v.adapter,v.base_url,v.enabled,JSON.stringify(v.metadata)]); await draftAfter(c,'create','provider',q.rows[0].id,v); return q.rows[0]; }), 201);
    if (r === 'accounts') return ok(await tx(async c => { const v = AccountSchema.parse(body); const sid = v.credential ? await insertSecret(c, 'account_credential', v.credential) : null; const metadata = { ...v.metadata, ...(v.proxy !== undefined ? { proxy: v.proxy } : {}) }; const q = await c.query('INSERT INTO accounts (provider_id,secret_record_id,name,display_name,base_url,enabled,priority,weight,max_concurrency,cost,metadata) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) RETURNING *', [v.provider_id,sid,v.name,v.display_name,v.base_url,v.enabled,v.priority,v.weight,v.max_concurrency,v.cost,JSON.stringify(metadata)]); await draftAfter(c,'create','account',q.rows[0].id,{...v,credential: v.credential ? '[redacted]' : undefined, proxy: v.proxy ? '[redacted]' : undefined}); return q.rows[0]; }), 201);
    if (r === 'models') return ok(await tx(async c => { const v = ModelSchema.parse(body); const q = await c.query('INSERT INTO model_aliases (alias,upstream_model,provider_id,routing_strategy,enabled,fallbacks) VALUES ($1,$2,$3,$4,$5,$6) RETURNING *', [v.alias,v.upstream_model,v.provider_id ?? null,v.routing_strategy,v.enabled,v.fallbacks ?? []]); await c.query('INSERT INTO model_pricing (model_alias_id,input_per_mtok,output_per_mtok) VALUES ($1,$2,$3)', [q.rows[0].id,v.input_per_mtok ?? 0,v.output_per_mtok ?? 0]); if ('accounts' in v && v.accounts) for (let i=0;i<v.accounts.length;i++) await c.query('INSERT INTO model_account_mappings (model_alias_id,account_id,position) VALUES ($1,$2,$3)', [q.rows[0].id,v.accounts[i],i]); await draftAfter(c,'create','model_alias',q.rows[0].id,v); return q.rows[0]; }), 201);
    if (r === 'routing') return ok(await tx(async c => { const v = RoutingSchema.parse(body); const q = await c.query('INSERT INTO routing_settings (id,strategy,sticky_ttl,max_attempts,resilience) VALUES (true,$1,$2,$3,$4) ON CONFLICT (id) DO UPDATE SET strategy=EXCLUDED.strategy, sticky_ttl=EXCLUDED.sticky_ttl, max_attempts=EXCLUDED.max_attempts, resilience=EXCLUDED.resilience RETURNING *', [v.strategy,v.sticky_ttl,v.max_attempts,JSON.stringify(v.resilience)]); await c.query(`INSERT INTO system_settings (key,value,updated_at) VALUES ('optimization',$1,now()) ON CONFLICT (key) DO UPDATE SET value=EXCLUDED.value, updated_at=now()`, [JSON.stringify({ rtk_compression: v.optimization.rtk_compression, caveman: v.optimization.caveman, headroom: v.optimization.headroom, headroom_reserve: v.optimization.headroom_reserve, headroom_keep: v.optimization.headroom_keep })]); await draftAfter(c,'update','routing_settings','singleton',v); return q.rows[0]; }));
    if (r === 'teams') return ok(await tx(async c => { const v = TeamSchema.parse(body); const q = await c.query('INSERT INTO teams (name,enabled,budget_usd) VALUES ($1,$2,$3) RETURNING *', [v.name,v.enabled,v.budget_usd]); await draftAfter(c,'create','team',q.rows[0].id,v); return q.rows[0]; }), 201);
    if (r === 'webhook') return ok(await tx(async c => { const v = WebhookSchema.parse(body); await c.query(`INSERT INTO system_settings (key,value,updated_at) VALUES ('webhook',$1,now()) ON CONFLICT (key) DO UPDATE SET value=EXCLUDED.value, updated_at=now()`, [JSON.stringify(v)]); await draftAfter(c,'update','webhook','singleton',v); return v; }));
    if (r === 'proxy-pool') return ok(await saveProxyPool(body));
    if (r === 'import') return ok(await tx(async c => { const snapshot = isRecord(body) && 'snapshot' in body ? (body as { snapshot: unknown }).snapshot : body; const result = await importSnapshot(c, snapshot); return result; }), 201);
    if (r === 'virtual-keys') return ok(await tx(async c => { const v = VirtualKeySchema.parse(body); const plaintext = v.plaintext_key ?? `sk-cp-${crypto.randomBytes(24).toString('base64url')}`; const hash = crypto.createHmac('sha256', process.env.GATEWAY_SHARED_SECRET ?? 'dev-secret-change-me').update(plaintext).digest('hex'); const q = await c.query('INSERT INTO virtual_keys (name,key_hash,key_prefix,enabled,models,rpm,tpm,max_concurrency,budget_usd,team_id) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) RETURNING id,name,key_prefix,enabled,models,rpm,tpm,max_concurrency,budget_usd,team_id,created_at', [v.name,hash,plaintext.slice(0,10),v.enabled,v.models,v.rpm,v.tpm,v.max_concurrency,v.budget_usd,v.team_id ?? null]); await draftAfter(c,'create','virtual_key',q.rows[0].id,{...v,plaintext_key:'[redacted]'}); return { ...q.rows[0], plaintext_key: plaintext }; }), 201);
    if (r === 'config-versions' && p[1] === 'publish') { const pub = await tx(publishLatest); await publishConfigVersion(pub.version, pub.checksum); return ok(pub); }
    if (r === 'config-versions' && p[1] === 'rollback') return ok(await tx(async c => { const source = Number((body as any).version); const old = await c.query('SELECT snapshot,status FROM config_versions WHERE version=$1', [source]); if (!old.rows[0]) throw new ApiError(404,'not_found','Source version not found'); if (old.rows[0].status === 'invalid') throw new ApiError(409,'invalid_source','Invalid config versions cannot be rollback sources'); const snapshot = old.rows[0].snapshot; const revs = await c.query('SELECT id, credential_revision FROM accounts WHERE id = ANY($1::uuid[])', [(snapshot.accounts ?? []).map((a: any) => a.id).filter(Boolean)]); const byId = new Map(revs.rows.map((x: any) => [x.id, Number(x.credential_revision)])); snapshot.accounts = (snapshot.accounts ?? []).map((a: any) => byId.has(a.id) ? { ...a, credential_revision: byId.get(a.id) } : a); const checksum = sha256Canonical(snapshot); const q = await c.query('INSERT INTO config_versions (status,checksum,config_checksum,schema_version,snapshot,source_version) VALUES ($1,$2,$2,$3,$4,$5) RETURNING version,checksum', ['draft',checksum,snapshot.version ?? 2,JSON.stringify(snapshot),source]); await audit(c,'rollback','config_version',String(q.rows[0].version),{source_version:source}); return q.rows[0]; }));
    throw new ApiError(404, 'not_found', `Unknown resource ${r}`);
  } catch (e) { return problem(e); }
}

export async function PATCH(req: Request, ctx: Ctx) {
  try {
    const p = await pathOf(ctx); const r = p[0]; const id = p[1]; const body = await json(req);
    if (!id) throw new ApiError(400, 'missing_id', 'Resource id is required');
    if (r === 'providers') return ok(await tx(async c => { const v = ProviderPatchSchema.parse(body); const q = await c.query('UPDATE providers SET name=COALESCE($2,name), adapter=COALESCE($3,adapter), base_url=COALESCE($4,base_url), enabled=COALESCE($5,enabled), metadata=COALESCE($6,metadata), updated_at=now() WHERE id=$1 RETURNING *', [id,v.name,v.adapter,v.base_url,v.enabled,v.metadata && JSON.stringify(v.metadata)]); await draftAfter(c,'update','provider',id,v); return q.rows[0]; }));
    if (r === 'accounts') return ok(await tx(async c => { const v = AccountPatchSchema.parse(body); const sid = v.credential ? await insertSecret(c, 'account_credential', v.credential) : undefined; let metadata = v.metadata; if (v.proxy !== undefined) { const current = (await c.query('SELECT metadata FROM accounts WHERE id=$1', [id])).rows[0]; metadata = { ...(current?.metadata ?? {}), proxy: v.proxy }; } const q = await c.query('UPDATE accounts SET secret_record_id=COALESCE($2,secret_record_id), display_name=COALESCE($3,display_name), base_url=COALESCE($4,base_url), enabled=COALESCE($5,enabled), priority=COALESCE($6,priority), weight=COALESCE($7,weight), max_concurrency=COALESCE($8,max_concurrency), cost=COALESCE($9,cost), metadata=COALESCE($10,metadata), updated_at=now() WHERE id=$1 RETURNING *', [id,sid,v.display_name,v.base_url,v.enabled,v.priority,v.weight,v.max_concurrency,v.cost,metadata && JSON.stringify(metadata)]); await draftAfter(c,'update','account',id,{...v,credential:v.credential?'[redacted]':undefined, proxy: v.proxy !== undefined ? '[redacted]' : undefined}); return q.rows[0]; }));
    if (r === 'models') return ok(await tx(async c => { const v = ModelPatchSchema.parse(body); if (v.routing_strategy) return updateProviderModelStrategy(c, id, v.routing_strategy); const q = await c.query('UPDATE model_aliases SET alias=COALESCE($2,alias), upstream_model=COALESCE($3,upstream_model), enabled=COALESCE($4,enabled), fallbacks=COALESCE($5,fallbacks), updated_at=now() WHERE id=$1 RETURNING *', [id,v.alias,v.upstream_model,v.enabled,v.fallbacks]); if (!q.rows[0]) throw new ApiError(404,'not_found','Model not found'); if (v.input_per_mtok !== undefined || v.output_per_mtok !== undefined) { const current = (await c.query('SELECT input_per_mtok,output_per_mtok FROM model_pricing WHERE model_alias_id=$1', [id])).rows[0]; await c.query(`INSERT INTO model_pricing (model_alias_id,input_per_mtok,output_per_mtok) VALUES ($1,$2,$3)
ON CONFLICT (model_alias_id) DO UPDATE SET input_per_mtok=EXCLUDED.input_per_mtok, output_per_mtok=EXCLUDED.output_per_mtok, updated_at=now()`, [id, v.input_per_mtok ?? Number(current?.input_per_mtok ?? 0), v.output_per_mtok ?? Number(current?.output_per_mtok ?? 0)]); } if (v.accounts) { await c.query('DELETE FROM model_account_mappings WHERE model_alias_id=$1', [id]); for (let i=0;i<v.accounts.length;i++) await c.query('INSERT INTO model_account_mappings (model_alias_id,account_id,position) VALUES ($1,$2,$3)', [id,v.accounts[i],i]); } await draftAfter(c,'update','model_alias',id,v); return q.rows[0]; }));
    if (r === 'teams') return ok(await tx(async c => { const v = TeamPatchSchema.parse(body); const q = await c.query('UPDATE teams SET name=COALESCE($2,name), enabled=COALESCE($3,enabled), budget_usd=COALESCE($4,budget_usd), updated_at=now() WHERE id=$1 RETURNING *', [id,v.name,v.enabled,v.budget_usd]); if (!q.rows[0]) throw new ApiError(404,'not_found','Team not found'); await draftAfter(c,'update','team',id,v); return q.rows[0]; }));
    if (r === 'virtual-keys') return ok(await tx(async c => { const v = VirtualKeyPatchSchema.parse(body); const hasTeam = Object.prototype.hasOwnProperty.call(v, 'team_id'); const q = await c.query(`UPDATE virtual_keys SET name=COALESCE($2,name), enabled=COALESCE($3,enabled), models=COALESCE($4,models), rpm=COALESCE($5,rpm), tpm=COALESCE($6,tpm), max_concurrency=COALESCE($7,max_concurrency), budget_usd=COALESCE($8,budget_usd)${hasTeam ? ', team_id=$9' : ''} WHERE id=$1 RETURNING id,name,key_prefix,enabled,models,rpm,tpm,max_concurrency,budget_usd,team_id,created_at`, [id,v.name,v.enabled,v.models,v.rpm,v.tpm,v.max_concurrency,v.budget_usd,...(hasTeam ? [v.team_id ?? null] : [])]); if (!q.rows[0]) throw new ApiError(404,'not_found','Virtual key not found'); await draftAfter(c,'update','virtual_key',id,v); return q.rows[0]; }));
    throw new ApiError(404, 'not_found', `Unknown resource ${r}`);
  } catch (e) { return problem(e); }
}

export async function DELETE(_req: Request, ctx: Ctx) {
  try {
    const p = await pathOf(ctx); const r = p[0]; const id = p[1]; if (!id) throw new ApiError(400, 'missing_id', 'Resource id is required');
    if (r === 'accounts') return ok(await tx(async c => { const result = await deleteAccountResource(c, id); await draftAfter(c, 'delete', 'account', id); return result; }));
    if (r === 'providers') return ok(await tx(async c => { const result = await deleteProviderResource(c, id); await draftAfter(c, 'delete', 'provider', id, { deleted_accounts: result.deleted_accounts }); return result; }));
    if (r === 'models') return ok(await tx(c => deleteModelRoute(c, id)));
    const table: Record<string,string> = { providers: 'providers', accounts: 'accounts', models: 'model_aliases', 'virtual-keys': 'virtual_keys', teams: 'teams' };
    if (!table[r]) throw new ApiError(404, 'not_found', `Unknown resource ${r}`);
    return ok(await tx(async c => { const q = await c.query(`UPDATE ${table[r]} SET enabled=false WHERE id=$1 RETURNING id`, [id]); await draftAfter(c,'disable',r,id); return q.rows[0] ?? { id }; }));
  } catch (e) { return problem(e); }
}
