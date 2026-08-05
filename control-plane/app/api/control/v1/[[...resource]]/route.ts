import crypto from 'node:crypto';
import { pool, tx } from '@/lib/db';
import { ok, problem, ApiError } from '@/lib/api';
import { publishConfigVersion } from '@/lib/redis';
import { AccountSchema, ProviderSchema, ModelSchema, RoutingSchema, VirtualKeySchema, audit, insertSecret, publishLatest, storeDraft } from '@/lib/control';

export const dynamic = 'force-dynamic';
type Ctx = { params: Promise<{ resource?: string[] }> };
const pathOf = async (ctx: Ctx) => (await ctx.params).resource ?? ['overview'];
const json = async (req: Request) => req.headers.get('content-length') === '0' ? {} : req.json();

async function draftAfter(client: any, action: string, typ: string, id?: string, payload?: unknown) { await audit(client, action, typ, id, payload); return storeDraft(client); }

export async function GET(_req: Request, ctx: Ctx) {
  try {
    const p = await pathOf(ctx); const r = p[0];
    if (r === 'overview') {
      const q = await pool.query(`SELECT (SELECT count(*) FROM providers) providers,(SELECT count(*) FROM accounts) accounts,(SELECT count(*) FROM model_aliases) models,(SELECT count(*) FROM virtual_keys) virtual_keys,(SELECT max(version) FROM config_versions WHERE status='published') published_version`);
      return ok(q.rows[0]);
    }
    if (r === 'providers') return ok((await pool.query('SELECT * FROM providers ORDER BY name')).rows);
    if (r === 'accounts') return ok((await pool.query(`SELECT a.*, sr.key_version, sr.rotated_at, sr.id secret_id FROM accounts a LEFT JOIN secret_records sr ON sr.id=a.secret_record_id ORDER BY a.name`)).rows.map(row => ({ ...row, secret_present: Boolean(row.secret_id) })));
    if (r === 'models') return ok((await pool.query(`SELECT ma.*, COALESCE(json_agg(mam.account_id ORDER BY mam.position) FILTER (WHERE mam.account_id IS NOT NULL),'[]') accounts FROM model_aliases ma LEFT JOIN model_account_mappings mam ON mam.model_alias_id=ma.id GROUP BY ma.id ORDER BY ma.alias`)).rows);
    if (r === 'gateway-acks') return ok((await pool.query('SELECT gateway_id,version,checksum,status,error,acknowledged_at FROM gateway_config_acks ORDER BY acknowledged_at DESC LIMIT 200')).rows.map(row => ({ ...row, version: Number(row.version) })));
    if (r === 'routing') return ok((await pool.query('SELECT * FROM routing_settings WHERE id=true')).rows[0]);
    if (r === 'virtual-keys') return ok((await pool.query('SELECT id,name,key_prefix,enabled,models,rpm,created_at,last_used_at FROM virtual_keys ORDER BY name')).rows);
    if (r === 'settings') return ok((await pool.query('SELECT * FROM system_settings ORDER BY key')).rows);
    if (r === 'audit-events') return ok((await pool.query('SELECT * FROM audit_events ORDER BY id DESC LIMIT 200')).rows);
    if (r === 'config-versions') return ok((await pool.query('SELECT version,status,checksum,errors,published_at,created_at,source_version FROM config_versions ORDER BY version DESC LIMIT 100')).rows);
    throw new ApiError(404, 'not_found', `Unknown resource ${r}`);
  } catch (e) { return problem(e); }
}

export async function POST(req: Request, ctx: Ctx) {
  try {
    const p = await pathOf(ctx); const r = p[0]; const body = await json(req);
    if (r === 'providers') return ok(await tx(async c => { const v = ProviderSchema.parse(body); const q = await c.query('INSERT INTO providers (slug,name,adapter,base_url,enabled,metadata) VALUES ($1,$2,$3,$4,$5,$6) RETURNING *', [v.slug,v.name,v.adapter,v.base_url,v.enabled,JSON.stringify(v.metadata)]); await draftAfter(c,'create','provider',q.rows[0].id,v); return q.rows[0]; }), 201);
    if (r === 'accounts') return ok(await tx(async c => { const v = AccountSchema.parse(body); const sid = v.credential ? await insertSecret(c, 'account_credential', v.credential) : null; const q = await c.query('INSERT INTO accounts (provider_id,secret_record_id,name,display_name,base_url,enabled,priority,weight,max_concurrency,cost,metadata) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) RETURNING *', [v.provider_id,sid,v.name,v.display_name,v.base_url,v.enabled,v.priority,v.weight,v.max_concurrency,v.cost,JSON.stringify(v.metadata)]); await draftAfter(c,'create','account',q.rows[0].id,{...v,credential: v.credential ? '[redacted]' : undefined}); return q.rows[0]; }), 201);
    if (r === 'models') return ok(await tx(async c => { const v = ModelSchema.parse(body); const q = await c.query('INSERT INTO model_aliases (alias,upstream_model,enabled) VALUES ($1,$2,$3) RETURNING *', [v.alias,v.upstream_model,v.enabled]); for (let i=0;i<v.accounts.length;i++) await c.query('INSERT INTO model_account_mappings (model_alias_id,account_id,position) VALUES ($1,$2,$3)', [q.rows[0].id,v.accounts[i],i]); await draftAfter(c,'create','model_alias',q.rows[0].id,v); return q.rows[0]; }), 201);
    if (r === 'routing') return ok(await tx(async c => { const v = RoutingSchema.parse(body); const q = await c.query('INSERT INTO routing_settings (id,strategy,sticky_ttl,max_attempts,resilience) VALUES (true,$1,$2,$3,$4) ON CONFLICT (id) DO UPDATE SET strategy=EXCLUDED.strategy, sticky_ttl=EXCLUDED.sticky_ttl, max_attempts=EXCLUDED.max_attempts, resilience=EXCLUDED.resilience RETURNING *', [v.strategy,v.sticky_ttl,v.max_attempts,JSON.stringify(v.resilience)]); await draftAfter(c,'update','routing_settings','singleton',v); return q.rows[0]; }));
    if (r === 'virtual-keys') return ok(await tx(async c => { const v = VirtualKeySchema.parse(body); const plaintext = v.plaintext_key ?? `sk-cp-${crypto.randomBytes(24).toString('base64url')}`; const hash = crypto.createHmac('sha256', process.env.GATEWAY_SHARED_SECRET ?? 'dev-secret-change-me').update(plaintext).digest('hex'); const q = await c.query('INSERT INTO virtual_keys (name,key_hash,key_prefix,enabled,models,rpm) VALUES ($1,$2,$3,$4,$5,$6) RETURNING id,name,key_prefix,enabled,models,rpm,created_at', [v.name,hash,plaintext.slice(0,10),v.enabled,v.models,v.rpm]); await draftAfter(c,'create','virtual_key',q.rows[0].id,{...v,plaintext_key:'[redacted]'}); return { ...q.rows[0], plaintext_key: plaintext }; }), 201);
    if (r === 'config-versions' && p[1] === 'publish') { const pub = await tx(publishLatest); await publishConfigVersion(pub.version, pub.checksum); return ok(pub); }
    if (r === 'config-versions' && p[1] === 'rollback') return ok(await tx(async c => { const source = Number((body as any).version); const old = await c.query('SELECT snapshot,checksum FROM config_versions WHERE version=$1', [source]); if (!old.rows[0]) throw new ApiError(404,'not_found','Source version not found'); const q = await c.query('INSERT INTO config_versions (status,checksum,snapshot,source_version) VALUES ($1,$2,$3,$4) RETURNING version,checksum', ['draft',old.rows[0].checksum,JSON.stringify(old.rows[0].snapshot),source]); await audit(c,'rollback','config_version',String(q.rows[0].version),{source_version:source}); return q.rows[0]; }));
    throw new ApiError(404, 'not_found', `Unknown resource ${r}`);
  } catch (e) { return problem(e); }
}

export async function PATCH(req: Request, ctx: Ctx) {
  try {
    const p = await pathOf(ctx); const r = p[0]; const id = p[1]; const body = await json(req);
    if (!id) throw new ApiError(400, 'missing_id', 'Resource id is required');
    if (r === 'providers') return ok(await tx(async c => { const v = ProviderSchema.partial().parse(body); const q = await c.query('UPDATE providers SET name=COALESCE($2,name), adapter=COALESCE($3,adapter), base_url=COALESCE($4,base_url), enabled=COALESCE($5,enabled), metadata=COALESCE($6,metadata), updated_at=now() WHERE id=$1 RETURNING *', [id,v.name,v.adapter,v.base_url,v.enabled,v.metadata && JSON.stringify(v.metadata)]); await draftAfter(c,'update','provider',id,v); return q.rows[0]; }));
    if (r === 'accounts') return ok(await tx(async c => { const v = AccountSchema.partial().parse(body); const sid = v.credential ? await insertSecret(c, 'account_credential', v.credential) : undefined; const q = await c.query('UPDATE accounts SET secret_record_id=COALESCE($2,secret_record_id), display_name=COALESCE($3,display_name), base_url=COALESCE($4,base_url), enabled=COALESCE($5,enabled), priority=COALESCE($6,priority), weight=COALESCE($7,weight), max_concurrency=COALESCE($8,max_concurrency), cost=COALESCE($9,cost), metadata=COALESCE($10,metadata), updated_at=now() WHERE id=$1 RETURNING *', [id,sid,v.display_name,v.base_url,v.enabled,v.priority,v.weight,v.max_concurrency,v.cost,v.metadata && JSON.stringify(v.metadata)]); await draftAfter(c,'update','account',id,{...v,credential:v.credential?'[redacted]':undefined}); return q.rows[0]; }));
    if (r === 'models') return ok(await tx(async c => { const v = ModelSchema.partial().parse(body); const q = await c.query('UPDATE model_aliases SET alias=COALESCE($2,alias), upstream_model=COALESCE($3,upstream_model), enabled=COALESCE($4,enabled), updated_at=now() WHERE id=$1 RETURNING *', [id,v.alias,v.upstream_model,v.enabled]); if (!q.rows[0]) throw new ApiError(404,'not_found','Model not found'); if (v.accounts) { await c.query('DELETE FROM model_account_mappings WHERE model_alias_id=$1', [id]); for (let i=0;i<v.accounts.length;i++) await c.query('INSERT INTO model_account_mappings (model_alias_id,account_id,position) VALUES ($1,$2,$3)', [id,v.accounts[i],i]); } await draftAfter(c,'update','model_alias',id,v); return q.rows[0]; }));
    if (r === 'virtual-keys') return ok(await tx(async c => { const v = VirtualKeySchema.partial().parse(body); const q = await c.query('UPDATE virtual_keys SET name=COALESCE($2,name), enabled=COALESCE($3,enabled), models=COALESCE($4,models), rpm=COALESCE($5,rpm) WHERE id=$1 RETURNING id,name,key_prefix,enabled,models,rpm,created_at', [id,v.name,v.enabled,v.models,v.rpm]); if (!q.rows[0]) throw new ApiError(404,'not_found','Virtual key not found'); await draftAfter(c,'update','virtual_key',id,v); return q.rows[0]; }));
    throw new ApiError(404, 'not_found', `Unknown resource ${r}`);
  } catch (e) { return problem(e); }
}

export async function DELETE(_req: Request, ctx: Ctx) {
  try {
    const p = await pathOf(ctx); const r = p[0]; const id = p[1]; if (!id) throw new ApiError(400, 'missing_id', 'Resource id is required');
    const table: Record<string,string> = { providers: 'providers', accounts: 'accounts', models: 'model_aliases', 'virtual-keys': 'virtual_keys' };
    if (!table[r]) throw new ApiError(404, 'not_found', `Unknown resource ${r}`);
    return ok(await tx(async c => { const q = await c.query(`UPDATE ${table[r]} SET enabled=false WHERE id=$1 RETURNING id`, [id]); await draftAfter(c,'disable',r,id); return q.rows[0] ?? { id }; }));
  } catch (e) { return problem(e); }
}
