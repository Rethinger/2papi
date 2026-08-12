import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import path from 'node:path';
import crypto from 'node:crypto';
import { Pool } from 'pg';
import { canonicalJson } from '../lib/canonical-json.ts';
import { assertSchemaV2Publishable, insertSecret, persistGatewayAck, publishLatest, storeDraft } from '../lib/control.ts';
import { credentialDigestFromDeclarative } from '../lib/snapshots.ts';
import { snapshotEnvelope } from '../lib/snapshot-envelope.ts';

const url = process.env.TEST_DATABASE_URL;
const options = { skip: url ? false : 'TEST_DATABASE_URL is not set' };
const schema = `snapshot_envelope_${process.pid}`;
const pool = url ? new Pool({ connectionString: `${url}?options=-c%20search_path%3D${schema},public`, max: 4 }) : null;

async function sql(name: string) {
  return fs.readFile(path.join(process.cwd(), 'migrations', name), 'utf8');
}

async function tx<T>(fn: (c: any) => Promise<T>) {
  const c = await pool!.connect();
  try {
    await c.query('BEGIN');
    const value = await fn(c);
    await c.query('COMMIT');
    return value;
  } catch (error) {
    await c.query('ROLLBACK');
    throw error;
  } finally {
    c.release();
  }
}

async function migrate() {
  await pool!.query(`DROP SCHEMA IF EXISTS ${schema} CASCADE; CREATE SCHEMA ${schema};`);
  await pool!.query((await sql('001_schema.sql')).replace('CREATE EXTENSION IF NOT EXISTS pgcrypto;', ''));
  await pool!.query(await sql('002_snapshot_security.sql'));
  await pool!.query(await sql('003_codex_provider.sql'));
  await pool!.query(await sql('004_gateway_ack_idempotency.sql'));
  await pool!.query(await sql('006_provider_model_pools.sql'));
}

async function seed(c: any, adapter = 'openai-compatible') {
  const provider = await c.query(
    'INSERT INTO providers (slug,name,adapter,base_url) VALUES ($1,$2,$3,$4) RETURNING id',
    [adapter, adapter, adapter, 'http://upstream:9001'],
  );
  const credential = adapter === 'openai-codex'
    ? { access_token: 'codex-access', refresh_token: 'codex-refresh', id_token: 'codex-id', expires_at: '2026-08-07T00:00:00Z', client_id: 'codex-client', chatgpt_account_id: 'acct-123' }
    : { api_key: `${adapter}-current-key` };
  const secret = await insertSecret(c, 'account_credential', credential);
  const account = await c.query(
    'INSERT INTO accounts (provider_id,secret_record_id,name,display_name,base_url) VALUES ($1,$2,$3,$4,$5) RETURNING id',
    [provider.rows[0].id, secret, `${adapter}-account`, adapter, 'http://upstream:9001'],
  );
  const model = await c.query(
    'INSERT INTO model_aliases (alias,upstream_model) VALUES ($1,$2) RETURNING id',
    [`${adapter}-model`, 'gpt-4o-mini'],
  );
  await c.query('INSERT INTO model_account_mappings (model_alias_id,account_id,position) VALUES ($1,$2,0)', [model.rows[0].id, account.rows[0].id]);
  await c.query("INSERT INTO routing_settings (id,strategy,sticky_ttl,max_attempts) VALUES (true,'balanced','1h',2) ON CONFLICT (id) DO NOTHING");
  await c.query('INSERT INTO virtual_keys (name,key_hash,key_prefix,models,rpm) VALUES ($1,$2,$3,$4,60)', [`${adapter}-key`, 'd'.repeat(64), 'sk-env', [`${adapter}-model`]]);
}

const checksum = (value: unknown) => crypto.createHash('sha256').update(canonicalJson(value)).digest('hex');

const v2Request = (gatewayId: string) => new Request('http://control/api/internal/v1/snapshot', {
  headers: {
    'x-gateway-id': gatewayId,
    'x-gateway-snapshot-schemas': '1,2',
    'x-gateway-envelope-version': '2',
  },
});

test.before(async () => { if (url) await migrate(); });

test('snapshot endpoint serves legacy and persisted-capability v2 envelopes with exact identities', options, async () => {
  await tx(async c => {
    await seed(c);
    const draft = await storeDraft(c);
    await c.query("UPDATE config_versions SET status='published', published_at=now() WHERE version=$1", [draft.version]);

    const legacy: any = await snapshotEnvelope(c, new Request('http://control/api/internal/v1/snapshot'));
    assert.equal(legacy.snapshot.version, 1);
    assert.equal(legacy.checksum, checksum(legacy.snapshot));

    const v2: any = await snapshotEnvelope(c, v2Request('gw-v2'));
    assert.equal(v2.schema_version, 2);
    assert.equal(v2.snapshot.version, 2);
    assert.equal(v2.runtime_checksum, checksum(v2.snapshot));
    assert.equal(v2.snapshot.accounts[0].credential.kind, 'api_key');
    assert.equal(v2.snapshot.accounts[0].credential.revision, 1);

    const declarative = (await c.query('SELECT snapshot FROM config_versions WHERE version=$1', [draft.version])).rows[0].snapshot;
    assert.equal(v2.credential_digest, credentialDigestFromDeclarative(declarative));
    assert.equal(v2.credential_digest, credentialDigestFromDeclarative({ ...declarative, accounts: [...declarative.accounts].reverse() }));
    const observation = (await c.query("SELECT supported_schemas,envelope_version FROM gateway_instances WHERE gateway_id='gw-v2'")).rows[0];
    assert.deepEqual(observation.supported_schemas, [1, 2]);
    assert.equal(observation.envelope_version, 2);
  });
});

test('v1-only active gateways block Codex publish and receive 426 until a complete v2 ack', options, async () => {
  await tx(async c => {
    await c.query('TRUNCATE gateway_config_acks,gateway_instances,config_versions,model_account_mappings,model_aliases,accounts,providers,secret_records,virtual_keys CASCADE');
    await seed(c, 'openai-codex');
    const accepted = await storeDraft(c);
    await c.query("UPDATE config_versions SET status='published', published_at=now() WHERE version=$1", [accepted.version]);
    const draft = await storeDraft(c);
    await c.query("INSERT INTO gateway_instances (gateway_id,supported_schemas,envelope_version,last_seen_at) VALUES ('gw-v1',ARRAY[1],1,now())");

    await assert.rejects(publishLatest(c), (error: any) => error.status === 426);
    assert.equal((await c.query("SELECT status FROM config_versions WHERE status='draft'")).rowCount, 1);

    await assert.rejects(
      snapshotEnvelope(c, new Request('http://control/api/internal/v1/snapshot', { headers: { 'x-gateway-id': 'gw-v1' } })),
      (error: any) => error.status === 426 && error.code === 'upgrade_required',
    );
    await c.query("UPDATE gateway_instances SET supported_schemas=ARRAY[1,2], envelope_version=2 WHERE gateway_id='gw-v1'");
    const adopted: any = await snapshotEnvelope(c, v2Request('gw-v1'));
    assert.equal(adopted.snapshot.accounts[0].credential.kind, 'oauth');
    assert.equal(adopted.snapshot.accounts[0].credential.revision, 1);
    await assert.rejects(
      persistGatewayAck(c, {
        gateway_id: 'gw-v1',
        version: Number(accepted.version),
        checksum: adopted.runtime_checksum,
        status: 'adopted',
        schema_version: adopted.schema_version,
        config_checksum: 'wrong-config-checksum',
        credential_digest: adopted.credential_digest,
        runtime_checksum: adopted.runtime_checksum,
        envelope_version: 2,
      }),
      (error: any) => error.status === 409 && error.code === 'ack_identity_mismatch',
    );
    await assert.rejects(
      persistGatewayAck(c, {
        gateway_id: 'gw-v1',
        version: Number(draft.version),
        checksum: adopted.runtime_checksum,
        status: 'adopted',
        schema_version: adopted.schema_version,
        config_checksum: adopted.config_checksum,
        credential_digest: adopted.credential_digest,
        runtime_checksum: adopted.runtime_checksum,
        envelope_version: 2,
      }),
      (error: any) => error.status === 409 && error.code === 'ack_version_not_published',
    );
    const acceptedAck = {
      gateway_id: 'gw-v1',
      version: Number(accepted.version),
      checksum: adopted.runtime_checksum,
      status: 'adopted' as const,
      schema_version: adopted.schema_version,
      config_checksum: adopted.config_checksum,
      credential_digest: adopted.credential_digest,
      runtime_checksum: adopted.runtime_checksum,
      envelope_version: 2,
    };
    const firstAck = await persistGatewayAck(c, acceptedAck);
    const retriedAck = await persistGatewayAck(c, acceptedAck);
    assert.equal(retriedAck.id, firstAck.id);
    assert.equal((await c.query("SELECT count(*)::int n FROM gateway_config_acks WHERE gateway_id='gw-v1' AND status='adopted'")).rows[0].n, 1);
    const ack = (await c.query("SELECT schema_version,config_checksum,credential_digest,runtime_checksum,envelope_version FROM gateway_config_acks WHERE gateway_id='gw-v1'")).rows[0];
    assert.deepEqual(
      { ...ack, schema_version: Number(ack.schema_version), envelope_version: Number(ack.envelope_version) },
      { schema_version: 2, config_checksum: adopted.config_checksum, credential_digest: adopted.credential_digest, runtime_checksum: adopted.runtime_checksum, envelope_version: 2 },
    );
    await assert.doesNotReject(assertSchemaV2Publishable(c));
    await assert.doesNotReject(publishLatest(c));
    assert.equal((await c.query('SELECT status FROM config_versions WHERE version=$1', [draft.version])).rows[0].status, 'published');

    await c.query("UPDATE gateway_instances SET supported_schemas=ARRAY[1], envelope_version=1 WHERE gateway_id='gw-v1'");
    await assert.rejects(assertSchemaV2Publishable(c), (error: any) => error.status === 426);
    await c.query("UPDATE gateway_instances SET supported_schemas=ARRAY[1,2], envelope_version=2 WHERE gateway_id='gw-v1'");

    await c.query("UPDATE gateway_instances SET last_seen_at=now() - interval '1 hour'");
    await assert.rejects(assertSchemaV2Publishable(c), (error: any) => error.status === 409);
  });
});

test('schema v1 rollback bypasses v2 gate and materializes current credentials', options, async () => {
  await tx(async c => {
    await c.query('TRUNCATE gateway_config_acks,gateway_instances,config_versions,model_account_mappings,model_aliases,accounts,providers,secret_records,virtual_keys CASCADE');
    await seed(c);
    const accountId = (await c.query("SELECT id FROM accounts WHERE name='openai-compatible-account'")).rows[0].id;
    const v1 = {
      version: 1,
      metadata: {},
      server: { addr: ':8080' },
      virtual_keys: [{ name: 'openai-compatible-key', key_hash: 'd'.repeat(64), models: ['openai-compatible-model'], rpm: 60 }],
      models: [{ alias: 'openai-compatible-model', upstream_model: 'gpt-4o-mini', accounts: ['openai-compatible-account'] }],
      accounts: [{ id: accountId, name: 'openai-compatible-account', base_url: 'http://upstream:9001', enabled: true, priority: 1, weight: 1, max_concurrency: 100, cost: 0 }],
      routing: { strategy: 'balanced', sticky_ttl: '1h', max_attempts: 2 },
      resilience: {},
    };
    await c.query("INSERT INTO config_versions (status,checksum,config_checksum,schema_version,snapshot,published_at) VALUES ('draft','old','old',1,$1,now())", [JSON.stringify(v1)]);
    await publishLatest(c);
    const envelope: any = await snapshotEnvelope(c, new Request('http://control/api/internal/v1/snapshot'));
    assert.equal(envelope.snapshot.accounts[0].api_key, 'openai-compatible-current-key');
  });
});

test.after(async () => { await pool?.end(); });
