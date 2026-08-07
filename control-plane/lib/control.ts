import { z } from 'zod';
import type { PoolClient } from 'pg';
import { decryptSecretJson, encryptSecretJson, type EncryptedSecretRecord } from './crypto';
import { compileDeclarativeSnapshot, credentialDigestForDeclarative, materializeLegacyRuntimeSnapshot, materializeRuntimeSnapshot } from './snapshots';
import { sha256Canonical } from './canonical-json';
import { env } from './env';
import { ApiError } from './api';

export const ProviderSchema = z.object({ slug: z.string().min(1), name: z.string().min(1), adapter: z.string().default('openai-compatible'), base_url: z.string().url(), enabled: z.boolean().default(true), metadata: z.record(z.string(), z.unknown()).default({}) });
export const AccountSchema = z.object({ provider_id: z.string().uuid(), name: z.string().min(1), display_name: z.string().min(1), base_url: z.string().url(), enabled: z.boolean().default(true), priority: z.number().int().default(1), weight: z.number().int().positive().default(1), max_concurrency: z.number().int().positive().default(100), cost: z.number().nonnegative().default(0), credential: z.object({ api_key: z.string().min(1) }).optional(), metadata: z.record(z.string(), z.unknown()).default({}) });
export const ModelSchema = z.object({ alias: z.string().min(1), upstream_model: z.string().min(1), enabled: z.boolean().default(true), accounts: z.array(z.string().uuid()).min(1) });
export const RoutingSchema = z.object({ strategy: z.enum(['balanced','priority','weighted']).default('balanced'), sticky_ttl: z.string().default('1h'), max_attempts: z.number().int().positive().default(2), resilience: z.object({ cooldown: z.string().default('30s'), circuit_failures: z.number().int().positive().default(3), circuit_reset: z.string().default('1m') }).default({ cooldown: '30s', circuit_failures: 3, circuit_reset: '1m' }) });
export const VirtualKeySchema = z.object({ name: z.string().min(1), plaintext_key: z.string().min(8).optional(), enabled: z.boolean().default(true), models: z.array(z.string()).default([]), rpm: z.number().int().positive().default(60) });

function b64ToBuf(v: string) { return Buffer.from(v, 'base64'); }
function bufToB64(v: Buffer) { return v.toString('base64'); }
export function rowToEncrypted(row: any): EncryptedSecretRecord { return { key_version: row.key_version, data_key_nonce: bufToB64(row.data_key_nonce), data_key_ciphertext: bufToB64(row.data_key_ciphertext), data_key_tag: bufToB64(row.data_key_tag), secret_nonce: bufToB64(row.secret_nonce), secret_ciphertext: bufToB64(row.secret_ciphertext), secret_tag: bufToB64(row.secret_tag) }; }

export async function insertSecret(client: PoolClient, purpose: string, credential: unknown) {
  const e = encryptSecretJson(credential);
  const r = await client.query(`INSERT INTO secret_records (purpose,key_version,data_key_nonce,data_key_ciphertext,data_key_tag,secret_nonce,secret_ciphertext,secret_tag) VALUES ($1,$2,$3,$4,$5,$6,$7,$8) RETURNING id`, [purpose, e.key_version, b64ToBuf(e.data_key_nonce), b64ToBuf(e.data_key_ciphertext), b64ToBuf(e.data_key_tag), b64ToBuf(e.secret_nonce), b64ToBuf(e.secret_ciphertext), b64ToBuf(e.secret_tag)]);
  return r.rows[0].id as string;
}

export async function audit(client: PoolClient, action: string, resourceType: string, resourceId?: string, payload: unknown = {}) {
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
  const draft = await client.query("SELECT * FROM config_versions WHERE status='draft' ORDER BY version DESC LIMIT 1 FOR UPDATE");
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
