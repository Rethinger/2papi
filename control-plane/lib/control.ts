import { z } from 'zod';
import type { PoolClient } from 'pg';
import { decryptSecretJson, encryptSecretJson, type EncryptedSecretRecord } from './crypto';import { compileDeclarativeSnapshot, credentialDigestForDeclarative, materializeLegacyRuntimeSnapshot, materializeRuntimeSnapshot } from './snapshots';
import { sha256Canonical } from './canonical-json';
import { env } from './env';
import { ApiError } from './api';
import { parseProxyList } from './proxylib';
import type { Queryable } from './db';

// SSRF guard: upstream endpoints must be public http(s). IP literals in
// private/loopback/link-local ranges are rejected unless
// ALLOW_PRIVATE_UPSTREAMS=true (needed for local test upstreams).
export function isPublicHttpUrl(value: string): boolean {
  if (env.ALLOW_PRIVATE_UPSTREAMS) return true;
  let url: URL;
  try { url = new URL(value); } catch { return false; }
  if (url.protocol !== 'http:' && url.protocol !== 'https:') return false;
  const host = url.hostname.toLowerCase().replace(/^\[|\]$/g, '');
  if (host === 'localhost' || host.endsWith('.localhost') || host === '0.0.0.0') return false;
  if (/^[0-9a-f:]+$/.test(host) && host.includes(':')) {
    if (host === '::1' || host === '::' || host.startsWith('fe80') || host.startsWith('fc') || host.startsWith('fd')) return false;
    return true;
  }
  const ipv4 = /^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$/.exec(host);
  if (ipv4) {
    const [a, b] = [Number(ipv4[1]), Number(ipv4[2])];
    if (a === 127 || a === 0 || a === 10 || a >= 224) return false;
    if (a === 169 && b === 254) return false;
    if (a === 172 && b >= 16 && b <= 31) return false;
    if (a === 192 && b === 168) return false;
  }
  return true;
}

const publicHttpUrl = (label: string) => (value: string, ctx: z.RefinementCtx) => {
  if (!isPublicHttpUrl(value)) {
    ctx.addIssue({ code: z.ZodIssueCode.custom, message: `${label} must be a public http(s) endpoint (private/loopback IPs blocked; set ALLOW_PRIVATE_UPSTREAMS=true to allow)` });
  }
};

const httpUrl = () => z.string().url().superRefine(publicHttpUrl('base_url'));

export const ProviderSchema = z.object({ slug: z.string().min(1), name: z.string().min(1), adapter: z.string().default('openai-compatible'), base_url: httpUrl(), enabled: z.boolean().default(true), metadata: z.record(z.string(), z.unknown()).default({}) });
export const AccountCredentialSchema = z.object({
  kind: z.enum(['api_key', 'cookie', 'oauth']).default('api_key'),
  api_key: z.string().min(1).optional(),
  cookies: z.string().min(1).optional(),
  access_token: z.string().min(1).optional(),
  refresh_token: z.string().optional(),
  expires_at: z.string().optional(),
  client_id: z.string().optional(),
  organization_id: z.string().optional(),
}).superRefine((credential, ctx) => {
  if (credential.kind === 'api_key' && !credential.api_key) {
    ctx.addIssue({ code: z.ZodIssueCode.custom, message: 'api_key is required for api_key credentials', path: ['api_key'] });
  }
  if (credential.kind === 'cookie' && !credential.cookies) {
    ctx.addIssue({ code: z.ZodIssueCode.custom, message: 'cookies are required for cookie credentials', path: ['cookies'] });
  }
  if (credential.kind === 'oauth' && !credential.access_token) {
    ctx.addIssue({ code: z.ZodIssueCode.custom, message: 'access_token is required for OAuth credentials', path: ['access_token'] });
  }
});

const proxyField = () => z.string().max(65536).optional().superRefine((value, ctx) => {
  if (!value || !value.trim()) return;
  const { errors } = parseProxyList(value);
  for (const e of errors.slice(0, 5)) {
    ctx.addIssue({ code: z.ZodIssueCode.custom, message: `line ${e.line}: ${e.text} — ${e.reason}` });
  }
});

export const AccountSchema = z.object({ provider_id: z.string().uuid(), name: z.string().min(1), display_name: z.string().min(1), base_url: httpUrl(), enabled: z.boolean().default(true), priority: z.number().int().default(1), weight: z.number().int().positive().default(1), max_concurrency: z.number().int().positive().default(100), cost: z.number().nonnegative().default(0), credential: AccountCredentialSchema.optional(), metadata: z.record(z.string(), z.unknown()).default({}), proxy: proxyField() });
const ModelBaseSchema = z.object({ alias: z.string().min(1), upstream_model: z.string().min(1), enabled: z.boolean().default(true), fallbacks: z.array(z.string().min(1)).default([]), input_per_mtok: z.number().nonnegative().default(0), output_per_mtok: z.number().nonnegative().default(0) });
const ManualModelSchema = ModelBaseSchema.extend({
  provider_id: z.null().optional(),
  routing_strategy: z.literal('manual').default('manual'),
  accounts: z.array(z.string().uuid()).min(1),
});
const ProviderModelSchema = ModelBaseSchema.extend({
  provider_id: z.string().uuid(),
  routing_strategy: z.enum(['round_robin', 'quota_failover', 'p2c', 'least_used', 'lkgp', 'reset_aware']),
  accounts: z.never().optional(),
});
export const ModelSchema = z.union([ProviderModelSchema, ManualModelSchema]);
export const RoutingSchema = z.object({ strategy: z.enum(['balanced','priority','weighted','p2c','least_used','lkgp','reset_aware','fastest','cheapest','quota_drain','adaptive']).default('balanced'), sticky_ttl: z.string().default('1h'), max_attempts: z.number().int().positive().default(2), resilience: z.object({ cooldown: z.string().default('30s'), circuit_failures: z.number().int().positive().default(3), circuit_reset: z.string().default('1m'), lockout_failures: z.number().int().nonnegative().default(10), lockout_duration: z.string().default('15m') }).default({ cooldown: '30s', circuit_failures: 3, circuit_reset: '1m', lockout_failures: 10, lockout_duration: '15m' }), optimization: z.object({ rtk_compression: z.boolean().default(false), caveman: z.boolean().default(false), headroom: z.boolean().default(false), headroom_reserve: z.number().int().positive().default(120000), headroom_keep: z.number().int().positive().default(8) }).default({ rtk_compression: false, caveman: false, headroom: false, headroom_reserve: 120000, headroom_keep: 8 }) });
export const VirtualKeySchema = z.object({ name: z.string().min(1), plaintext_key: z.string().min(8).optional(), enabled: z.boolean().default(true), models: z.array(z.string()).default([]), rpm: z.number().int().positive().default(60), tpm: z.number().int().nonnegative().default(0), max_concurrency: z.number().int().nonnegative().default(0), budget_usd: z.number().nonnegative().default(0), team_id: z.string().uuid().optional().nullable() });
export const TeamSchema = z.object({ name: z.string().min(1), enabled: z.boolean().default(true), budget_usd: z.number().nonnegative().default(0), org_id: z.string().uuid().optional().nullable() });
export const WebhookSchema = z.object({ enabled: z.boolean().default(false), url: z.string().url().or(z.literal('')).default(''), secret: z.string().default('') });
export const TeamPatchSchema = z.object({ name: z.string().min(1).optional(), enabled: z.boolean().optional(), budget_usd: z.number().nonnegative().optional(), org_id: z.string().uuid().nullable().optional() });

// Enterprise (migration 015/016): organizations above teams; org budget
// caps every team budget under it (see internal/policy + snapshots).
export const OrganizationSchema = z.object({ name: z.string().min(1), owner_user_id: z.string().uuid().optional().nullable(), budget_usd: z.number().nonnegative().default(0) });
export const OrganizationPatchSchema = z.object({ name: z.string().min(1).optional(), owner_user_id: z.string().uuid().nullable().optional(), budget_usd: z.number().nonnegative().optional() });

// Zod 4 applies defaults inside `.partial()`. PATCH schemas must therefore be
// explicit: omitted properties have to remain omitted or a narrow update can
// silently reset unrelated persisted state (for example Codex auth metadata).
export const ProviderPatchSchema = z.object({
  name: z.string().min(1).optional(),
  adapter: z.string().min(1).optional(),
  base_url: httpUrl().optional(),
  enabled: z.boolean().optional(),
  metadata: z.record(z.string(), z.unknown()).optional(),
});
export const AccountPatchSchema = z.object({
  display_name: z.string().min(1).optional(),
  base_url: httpUrl().optional(),
  enabled: z.boolean().optional(),
  priority: z.number().int().optional(),
  weight: z.number().int().positive().optional(),
  max_concurrency: z.number().int().positive().optional(),
  cost: z.number().nonnegative().optional(),
  credential: AccountCredentialSchema.optional(),
  metadata: z.record(z.string(), z.unknown()).optional(),
  proxy: proxyField(),
});
export const ModelPatchSchema = z.object({
  alias: z.string().min(1).optional(),
  upstream_model: z.string().min(1).optional(),
  enabled: z.boolean().optional(),
  accounts: z.array(z.string().uuid()).min(1).optional(),
  routing_strategy: z.enum(['round_robin', 'quota_failover', 'p2c', 'least_used', 'lkgp', 'reset_aware']).optional(),
  fallbacks: z.array(z.string().min(1)).optional(),
  input_per_mtok: z.number().nonnegative().optional(),
  output_per_mtok: z.number().nonnegative().optional(),
});
export const VirtualKeyPatchSchema = z.object({
  name: z.string().min(1).optional(),
  enabled: z.boolean().optional(),
  models: z.array(z.string()).optional(),
  rpm: z.number().int().positive().optional(),
  tpm: z.number().int().nonnegative().optional(),
  max_concurrency: z.number().int().nonnegative().optional(),
  budget_usd: z.number().nonnegative().optional(),
  team_id: z.string().uuid().optional().nullable(),
});

function b64ToBuf(v: string) { return Buffer.from(v, 'base64'); }
function bufToB64(v: Buffer) { return v.toString('base64'); }
export function rowToEncrypted(row: any): EncryptedSecretRecord { return { key_version: row.key_version, data_key_nonce: bufToB64(row.data_key_nonce), data_key_ciphertext: bufToB64(row.data_key_ciphertext), data_key_tag: bufToB64(row.data_key_tag), secret_nonce: bufToB64(row.secret_nonce), secret_ciphertext: bufToB64(row.secret_ciphertext), secret_tag: bufToB64(row.secret_tag) }; }

export async function insertSecret(client: PoolClient, purpose: string, credential: unknown) {
  const e = encryptSecretJson(credential);
  const r = await client.query(`INSERT INTO secret_records (purpose,key_version,data_key_nonce,data_key_ciphertext,data_key_tag,secret_nonce,secret_ciphertext,secret_tag) VALUES ($1,$2,$3,$4,$5,$6,$7,$8) RETURNING id`, [purpose, e.key_version, b64ToBuf(e.data_key_nonce), b64ToBuf(e.data_key_ciphertext), b64ToBuf(e.data_key_tag), b64ToBuf(e.secret_nonce), b64ToBuf(e.secret_ciphertext), b64ToBuf(e.secret_tag)]);
  return r.rows[0].id as string;
}

export async function audit(client: Queryable, action: string, resourceType: string, resourceId?: string, payload: unknown = {}) {
  await client.query('INSERT INTO audit_events (action, resource_type, resource_id, payload) VALUES ($1,$2,$3,$4)', [action, resourceType, resourceId ?? null, JSON.stringify(payload)]);
}

export async function compileSnapshot(client: PoolClient) {
  const compiled = await compileDeclarativeSnapshot(client);
  const runtime = await materializeLegacyRuntimeSnapshot(client, compiled.snapshot);
  return { snapshot: runtime, checksum: sha256Canonical(runtime) };
}

export async function storeDraft(client: PoolClient) {
  const compiled = await compileDeclarativeSnapshot(client);
  const r = await client.query('INSERT INTO config_versions (status, checksum, config_checksum, schema_version, snapshot) VALUES ($1,$2,$2,$3,$4) RETURNING *', ['draft', compiled.checksum, compiled.schemaVersion, JSON.stringify(compiled.snapshot)]);
  return r.rows[0];
}

export async function publishLatest(client: PoolClient) {
  const draft = await client.query(`SELECT * FROM config_versions
    WHERE status='draft'
      AND version > COALESCE((SELECT max(version) FROM config_versions WHERE status='published'), 0)
    ORDER BY version DESC LIMIT 1 FOR UPDATE`);
  const row = draft.rows[0] ?? (await storeDraft(client));
  if (requiresSchemaV2(row.snapshot, Number(row.schema_version ?? row.snapshot?.version ?? 1))) await assertSchemaV2Publishable(client);
  const version = row.version;
  await client.query("UPDATE config_versions SET status='published', published_at=now() WHERE version=$1", [version]);
  await audit(client, 'publish', 'config_version', String(version), { checksum: row.checksum });
  return { version: Number(version), checksum: row.checksum };
}

export function gatewayCapabilityTtlSeconds() { return env.GATEWAY_CAPABILITY_TTL_SECONDS ?? env.SNAPSHOT_POLL_INTERVAL_SECONDS * 3; }

export async function upsertGatewayHeartbeat(client: PoolClient, input: { gateway_id: string; supported_schemas: number[]; envelope_version: number }) {
  await client.query(`INSERT INTO gateway_instances (gateway_id,supported_schemas,envelope_version,last_seen_at) VALUES ($1,$2,$3,now()) ON CONFLICT (gateway_id) DO UPDATE SET supported_schemas=EXCLUDED.supported_schemas,envelope_version=EXCLUDED.envelope_version,last_seen_at=now()`, [input.gateway_id, input.supported_schemas, input.envelope_version]);
}

export async function persistGatewayAck(client: PoolClient, input: {
  gateway_id: string;
  version: number;
  checksum: string;
  status: 'adopted' | 'rejected';
  error?: string;
  schema_version?: number;
  config_checksum?: string;
  credential_digest?: string;
  runtime_checksum?: string;
  envelope_version?: number;
}) {
  if ((input.envelope_version ?? 1) >= 2) {
    const gateway = (await client.query('SELECT supported_schemas,envelope_version FROM gateway_instances WHERE gateway_id=$1 FOR UPDATE', [input.gateway_id])).rows[0];
    if (!gateway || Number(gateway.envelope_version) < 2 || !(gateway.supported_schemas ?? []).includes(input.schema_version ?? 0)) {
      throw new ApiError(409, 'gateway_capability_mismatch', 'Acknowledgement exceeds the persisted gateway capability');
    }
    const version = (await client.query('SELECT status,schema_version,config_checksum,checksum,snapshot FROM config_versions WHERE version=$1', [input.version])).rows[0];
    if (!version) throw new ApiError(404, 'config_version_not_found', 'Acknowledged configuration version does not exist');
    if (version.status !== 'published') {
      throw new ApiError(409, 'ack_version_not_published', 'Acknowledged configuration version is not published');
    }
    const expectedCredentialDigest = await credentialDigestForDeclarative(client, version.snapshot);
    const runtime = await materializeRuntimeSnapshot(client, version.snapshot);
    const expectedRuntimeChecksum = sha256Canonical(runtime);
    if (
      Number(version.schema_version) !== input.schema_version ||
      (version.config_checksum ?? version.checksum) !== input.config_checksum ||
      expectedCredentialDigest !== input.credential_digest ||
      expectedRuntimeChecksum !== input.runtime_checksum
    ) {
      throw new ApiError(409, 'ack_identity_mismatch', 'Acknowledgement identity does not match the materialized configuration');
    }
  }
  const q = await client.query(
    `INSERT INTO gateway_config_acks
     (gateway_id,version,checksum,status,error,schema_version,config_checksum,credential_digest,runtime_checksum,envelope_version)
     VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
     ON CONFLICT (
       gateway_id,
       version,
       status,
       schema_version,
       (COALESCE(config_checksum, checksum)),
       (COALESCE(credential_digest, '')),
       (COALESCE(runtime_checksum, checksum)),
       envelope_version
     ) DO UPDATE SET
       error=EXCLUDED.error,
       acknowledged_at=now()
     RETURNING *`,
    [input.gateway_id, input.version, input.checksum, input.status, input.error ?? null, input.schema_version ?? 1, input.config_checksum ?? input.checksum, input.credential_digest ?? null, input.runtime_checksum ?? input.checksum, input.envelope_version ?? 1],
  );
  return q.rows[0];
}

export async function assertSchemaV2Publishable(client: PoolClient) {
  const active = await client.query(`SELECT gateway_id,supported_schemas,envelope_version FROM gateway_instances WHERE last_seen_at >= now() - ($1::int * interval '1 second') ORDER BY gateway_id FOR UPDATE`, [gatewayCapabilityTtlSeconds()]);
  if (active.rows.length < env.MIN_ACTIVE_GATEWAYS) throw new ApiError(409, 'insufficient_active_gateways', `At least ${env.MIN_ACTIVE_GATEWAYS} active gateway(s) required`);
  for (const g of active.rows) {
    if (Number(g.envelope_version) < 2 || !(g.supported_schemas ?? []).includes(2)) throw new ApiError(426, 'upgrade_required', `Gateway ${g.gateway_id} does not advertise schema v2 envelope support`);
    const ack = await client.query(`SELECT envelope_version,schema_version,status FROM gateway_config_acks WHERE gateway_id=$1 ORDER BY acknowledged_at DESC,id DESC LIMIT 1`, [g.gateway_id]);
    const row = ack.rows[0];
    if (!row || row.status !== 'adopted' || Number(row.envelope_version) < 2 || Number(row.schema_version) < 2) throw new ApiError(426, 'upgrade_required', `Gateway ${g.gateway_id} has not adopted schema v2 envelope`);
  }
}

function requiresSchemaV2(snapshot: any, schemaVersion: number) {
  return schemaVersion >= 2 || (snapshot?.accounts ?? []).some((a: any) => a.adapter === 'codex' || a.auth_type === 'codex' || a.credential_revision !== undefined);
}
