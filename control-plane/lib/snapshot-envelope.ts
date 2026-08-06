import crypto from 'node:crypto';
import type { PoolClient } from 'pg';
import { ApiError } from './api';
import { canonicalJson } from './canonical-json';
import { upsertGatewayHeartbeat } from './control';
import { credentialDigestFromDeclarative, materializeLegacyRuntimeSnapshot, materializeRuntimeSnapshot } from './snapshots';

export async function snapshotEnvelope(client: PoolClient, req: Request) {
  const query = await client.query(
    "SELECT version,schema_version,config_checksum,checksum,snapshot FROM config_versions WHERE status='published' ORDER BY version DESC LIMIT 1",
  );
  const row = query.rows[0];
  if (!row) return null;

  const gatewayId = req.headers.get('x-gateway-id') ?? undefined;
  const advertisedSchemas = parseSchemas(req.headers.get('x-gateway-snapshot-schemas'));
  const advertisedEnvelope = Number(req.headers.get('x-gateway-envelope-version') ?? 1);
  if (gatewayId && advertisedSchemas.length > 0 && Number.isInteger(advertisedEnvelope)) {
    await upsertGatewayHeartbeat(client, {
      gateway_id: gatewayId,
      supported_schemas: advertisedSchemas,
      envelope_version: advertisedEnvelope,
    });
  }

  let schemas = [1];
  let envelopeVersion = 1;
  if (gatewayId) {
    const persisted = await client.query(
      'SELECT supported_schemas,envelope_version FROM gateway_instances WHERE gateway_id=$1',
      [gatewayId],
    );
    if (persisted.rows[0]) {
      schemas = persisted.rows[0].supported_schemas ?? [1];
      envelopeVersion = Number(persisted.rows[0].envelope_version ?? 1);
    }
  }

  const schemaVersion = Number(row.schema_version ?? row.snapshot?.version ?? 1);
  const v2Capable = envelopeVersion >= 2 && schemas.includes(2);
  const v2Only = (row.snapshot?.accounts ?? []).some(
    (account: any) => account.adapter && account.adapter !== 'openai-compatible',
  );
  if (v2Only && !v2Capable) {
    throw new ApiError(426, 'upgrade_required', 'Published snapshot requires schema version 2 support');
  }

  const v2 = v2Capable && schemaVersion >= 2;
  const snapshot = v2
    ? await materializeRuntimeSnapshot(client, row.snapshot)
    : await materializeLegacyRuntimeSnapshot(client, row.snapshot);
  const runtimeChecksum = crypto.createHash('sha256').update(canonicalJson(snapshot)).digest('hex');

  if (!v2) {
    return { version: Number(row.version), checksum: runtimeChecksum, snapshot };
  }
  return {
    config_version: Number(row.version),
    schema_version: schemaVersion,
    config_checksum: row.config_checksum ?? row.checksum,
    credential_digest: credentialDigestFromDeclarative(snapshot),
    runtime_checksum: runtimeChecksum,
    snapshot,
  };
}

function parseSchemas(value: string | null) {
  return value === null
    ? []
    : value.split(',').map(part => Number(part.trim())).filter(Number.isInteger);
}
