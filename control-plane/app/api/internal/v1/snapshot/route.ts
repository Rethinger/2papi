import { NextResponse } from 'next/server';
import crypto from 'node:crypto';
import { pool } from '@/lib/db';
import { ApiError, problem, requireInternal } from '@/lib/api';
import { env } from '@/lib/env';
import { materializeLegacyRuntimeSnapshot, materializeRuntimeSnapshot } from '@/lib/snapshots';
import { canonicalJson, sha256Canonical } from '@/lib/canonical-json';

export const dynamic = 'force-dynamic';

export async function GET(req: Request) {
  try {
    requireInternal(req, env.INTERNAL_SERVICE_TOKEN);
    const client = await pool.connect();
    try {
      const row = await snapshotEnvelope(client, req);
      if (!row) throw new ApiError(404, 'not_found', 'Snapshot not found');
      return NextResponse.json(row);
    } finally { client.release(); }
  } catch (error) {
    return problem(error);
  }
}

async function snapshotEnvelope(client: any, req: Request) {
  const q = await client.query("SELECT version,schema_version,config_checksum,checksum,snapshot FROM config_versions WHERE status='published' ORDER BY version DESC LIMIT 1");
  const row = q.rows[0];
  if (!row) return null;
  const gatewayId = req.headers.get('x-gateway-id') ?? undefined;
  let schemas = parseSchemas(req.headers.get('x-gateway-snapshot-schemas'));
  let envelope = Number(req.headers.get('x-gateway-envelope-version') ?? 1);
  if (gatewayId) {
    const persisted = await client.query('SELECT supported_schemas,envelope_version FROM gateway_instances WHERE gateway_id=$1', [gatewayId]);
    if (persisted.rows[0]) { schemas = persisted.rows[0].supported_schemas ?? schemas; envelope = Number(persisted.rows[0].envelope_version ?? envelope); }
  }
  const schemaVersion = Number(row.schema_version ?? row.snapshot?.version ?? 1);
  const v2 = envelope >= 2 && schemas.includes(2) && schemaVersion >= 2;
  const snapshot = v2 ? await materializeRuntimeSnapshot(client, row.snapshot) : await materializeLegacyRuntimeSnapshot(client, row.snapshot);
  const runtime_checksum = crypto.createHash('sha256').update(canonicalJson(snapshot)).digest('hex');
  if (!v2) return { version: Number(row.version), checksum: runtime_checksum, snapshot };
  return { config_version: Number(row.version), schema_version: schemaVersion, config_checksum: row.config_checksum ?? row.checksum, credential_digest: sha256Canonical((snapshot.accounts ?? []).map((a: any) => ({ id: a.id ?? a.name, credential: a.credential ?? a.api_key ?? null }))), runtime_checksum, snapshot };
}

function parseSchemas(value: string | null) { return (value ?? '1').split(',').map(v => Number(v.trim())).filter(Number.isFinite); }
