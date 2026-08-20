import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import path from 'node:path';
import { Pool } from 'pg';

const url = process.env.TEST_DATABASE_URL;
const options = { skip: url ? false : 'TEST_DATABASE_URL is not set' };
const schema = `credential_cas_${process.pid}`;
const dbUrl = url ? `${url}?options=-c%20search_path%3D${schema},public` : '';
if (url) process.env.DATABASE_URL = dbUrl;
const pool = url ? new Pool({ connectionString: dbUrl, max: 4 }) : null;
const { decryptSecretJson, encryptSecretJson } = await import('../lib/crypto.ts');
let providerID = '';

async function sql(name: string) {
  return fs.readFile(path.join(process.cwd(), 'migrations', name), 'utf8');
}

async function insertEncrypted(client: any, value: unknown) {
  const encrypted = encryptSecretJson(value);
  const result = await client.query(`INSERT INTO secret_records
    (purpose,key_version,data_key_nonce,data_key_ciphertext,data_key_tag,secret_nonce,secret_ciphertext,secret_tag)
    VALUES ('account_credential',$1,$2,$3,$4,$5,$6,$7) RETURNING id`, [
    encrypted.key_version,
    Buffer.from(encrypted.data_key_nonce, 'base64'),
    Buffer.from(encrypted.data_key_ciphertext, 'base64'),
    Buffer.from(encrypted.data_key_tag, 'base64'),
    Buffer.from(encrypted.secret_nonce, 'base64'),
    Buffer.from(encrypted.secret_ciphertext, 'base64'),
    Buffer.from(encrypted.secret_tag, 'base64'),
  ]);
  return result.rows[0].id;
}

function encryptedRow(row: any) {
  return {
    key_version: row.key_version,
    data_key_nonce: row.data_key_nonce.toString('base64'),
    data_key_ciphertext: row.data_key_ciphertext.toString('base64'),
    data_key_tag: row.data_key_tag.toString('base64'),
    secret_nonce: row.secret_nonce.toString('base64'),
    secret_ciphertext: row.secret_ciphertext.toString('base64'),
    secret_tag: row.secret_tag.toString('base64'),
  };
}

test.before(async () => {
  if (!url) return;
  await pool!.query(`DROP SCHEMA IF EXISTS ${schema} CASCADE; CREATE SCHEMA ${schema};`);
  await pool!.query((await sql('001_schema.sql')).replace('CREATE EXTENSION IF NOT EXISTS pgcrypto;', ''));
  await pool!.query(await sql('002_snapshot_security.sql'));
  await pool!.query(await sql('003_codex_provider.sql'));
  const provider = await pool!.query("INSERT INTO providers (slug,name,adapter,base_url) VALUES ('cas','CAS','openai-codex','https://chatgpt.com/backend-api/codex') RETURNING id");
  providerID = provider.rows[0].id;
});

test('credential CAS updates revision, encrypts secret, audits safe fields, and rejects replay', options, async () => {
  const client = await pool!.connect();
  const firstSecret = await insertEncrypted(client, { access_token: 'old-token', chatgpt_account_id: 'acct', kind: 'oauth' });
  const account = await client.query("INSERT INTO accounts (provider_id,secret_record_id,name,display_name,base_url,credential_revision) VALUES ($1,$2,'cas-account','CAS Account','https://chatgpt.com/backend-api/codex',7) RETURNING id", [providerID, firstSecret]);
  client.release();

  const { PUT } = await import('../app/api/internal/v1/accounts/[id]/credentials/route.ts');
  const body = {
    expected_revision: 7,
    credential: {
      kind: 'oauth',
      access_token: 'new-access-token',
      refresh_token: 'new-refresh-token',
      id_token: 'new-id-token',
      expires_at: '2026-08-08T00:00:00.000Z',
      chatgpt_account_id: 'acct',
      revision: 7,
    },
  };
  const request = (expectedRevision = 7, includeGateway = true, token = process.env.INTERNAL_SERVICE_TOKEN ?? 'dev-internal-service-token') => new Request(
    `http://local/api/internal/v1/accounts/${account.rows[0].id}/credentials`,
    {
      method: 'PUT',
      headers: {
        authorization: `Bearer ${token}`,
        ...(includeGateway ? { 'x-gateway-id': 'gateway-test' } : {}),
        'content-type': 'application/json',
      },
      body: JSON.stringify({ ...body, expected_revision: expectedRevision }),
    },
  );

  const response = await PUT(request() as any, { params: { id: account.rows[0].id } });
  assert.equal(response.status, 200);
  const responseData = (await response.json()).data;
  assert.equal(responseData.credential_revision, 8);
  assert.match(responseData.credential_digest, /^[a-f0-9]{64}$/);

  const stored = await pool!.query(`SELECT a.credential_revision, a.secret_record_id, sr.*
    FROM accounts a JOIN secret_records sr ON sr.id=a.secret_record_id WHERE a.id=$1`, [account.rows[0].id]);
  assert.equal(Number(stored.rows[0].credential_revision), 8);
  const plaintext = decryptSecretJson<any>(encryptedRow(stored.rows[0]));
  assert.equal(plaintext.access_token, 'new-access-token');
  assert.equal(plaintext.refresh_token, 'new-refresh-token');
  assert.equal(plaintext.revision, undefined);
  assert.ok(!JSON.stringify(stored.rows[0]).includes('new-access-token'));

  const audit = await pool!.query("SELECT actor,payload FROM audit_events WHERE resource_id=$1 AND action='credential.updated'", [account.rows[0].id]);
  assert.equal(audit.rows[0].actor, 'gateway:gateway-test');
  assert.deepEqual(audit.rows[0].payload, { credential_revision: 8 });

  const replay = await PUT(request() as any, { params: { id: account.rows[0].id } });
  assert.equal(replay.status, 409);
  assert.equal((await replay.json()).error.code, 'credential_revision_conflict');
  assert.equal((await PUT(request(8, false) as any, { params: { id: account.rows[0].id } })).status, 403);
  assert.equal((await PUT(request(8, true, 'wrong-internal-token') as any, { params: { id: account.rows[0].id } })).status, 401);
});

test('credential CAS rejects oversized request before JSON parsing', options, async () => {
  const { PUT } = await import('../app/api/internal/v1/accounts/[id]/credentials/route.ts');
  const request = new Request('http://local/api/internal/v1/accounts/00000000-0000-0000-0000-000000000000/credentials', {
    method: 'PUT',
    headers: {
      authorization: `Bearer ${process.env.INTERNAL_SERVICE_TOKEN ?? 'dev-internal-service-token'}`,
      'x-gateway-id': 'gateway-test',
      'content-type': 'application/json',
    },
    body: JSON.stringify({ expected_revision: 1, credential: { kind: 'oauth', access_token: 'x'.repeat(300 * 1024) } }),
  });
  const response = await PUT(request as any, { params: { id: '00000000-0000-0000-0000-000000000000' } });
  assert.equal(response.status, 413);
});

test('credential CAS requires application/json content type', options, async () => {
  const { PUT } = await import('../app/api/internal/v1/accounts/[id]/credentials/route.ts');
  const request = new Request('http://local/api/internal/v1/accounts/00000000-0000-0000-0000-000000000000/credentials', {
    method: 'PUT',
    headers: {
      authorization: `Bearer ${process.env.INTERNAL_SERVICE_TOKEN ?? 'dev-internal-service-token'}`,
      'x-gateway-id': 'gateway-test',
      'content-type': 'text/plain',
    },
    body: '{}',
  });
  const response = await PUT(request as any, { params: { id: '00000000-0000-0000-0000-000000000000' } });
  assert.equal(response.status, 415);
  assert.equal((await response.json()).error.code, 'unsupported_media_type');
});

test('one-shot provider dispatch works before publish and does not retain or expose credentials', options, async () => {
  const client = await pool!.connect();
  const secret = await insertEncrypted(client, {
    kind: 'oauth',
    access_token: 'one-shot-access-token',
    refresh_token: 'one-shot-refresh-token',
    id_token: 'one-shot-id-token',
    chatgpt_account_id: 'acct-one-shot',
    plan_type: 'plus',
    auth_method: 'auth_file',
    email: 'must-not-cross-runtime-boundary@example.com',
  });
  const account = await client.query("INSERT INTO accounts (provider_id,secret_record_id,name,display_name,base_url,credential_revision) VALUES ($1,$2,'unpublished-account','Unpublished','https://chatgpt.com/backend-api/codex',3) RETURNING id", [providerID, secret]);
  client.release();

  const originalFetch = globalThis.fetch;
  const requests: Array<{ url: string; authorization: string | null; body: string }> = [];
  const { dispatchProviderOperation } = await import('../lib/provider-operations.ts');
  globalThis.fetch = async (input, init) => {
    requests.push({
      url: String(input),
      authorization: new Headers(init?.headers).get('authorization'),
      body: String(init?.body ?? ''),
    });
    return new Response(JSON.stringify({ data: { models: ['gpt-test'] }, warning_code: '', credential_revision: 3 }), {
      status: 200,
      headers: { 'content-type': 'application/json' },
    });
  };
  try {
    const result = await dispatchProviderOperation(pool!, account.rows[0].id, 'discover_models', {});
    assert.deepEqual(result.data, { models: ['gpt-test'] });
  } finally {
    globalThis.fetch = originalFetch;
  }

  assert.equal(requests.length, 1);
  assert.equal(requests[0].url, `${process.env.GATEWAY_INTERNAL_URL ?? 'http://127.0.0.1:8081'}/internal/v1/provider-operations`);
  assert.equal(requests[0].authorization, `Bearer ${process.env.INTERNAL_SERVICE_TOKEN ?? 'dev-internal-service-token'}`);
  const wire = JSON.parse(requests[0].body);
  assert.equal(wire.account.id, account.rows[0].id);
  assert.equal(wire.account.credential.access_token, 'one-shot-access-token');
  assert.equal(wire.account.credential.refresh_token, 'one-shot-refresh-token');
  assert.equal(wire.account.credential.id_token, 'one-shot-id-token');
  assert.equal(wire.account.credential.revision, 3);
  assert.equal(wire.account.credential.plan_type, undefined);
  assert.equal(wire.account.credential.auth_method, undefined);
  assert.equal(wire.account.credential.email, undefined);
  assert.equal((await pool!.query('SELECT count(*)::int n FROM config_versions')).rows[0].n, 0);

  const persisted = JSON.stringify((await pool!.query('SELECT * FROM accounts WHERE id=$1', [account.rows[0].id])).rows[0]);
  for (const secretValue of ['one-shot-access-token', 'one-shot-refresh-token', 'one-shot-id-token']) {
    assert.ok(!persisted.includes(secretValue));
  }

  const logged: string[] = [];
  const originalConsole = { log: console.log, error: console.error, warn: console.warn };
  console.log = (...args: unknown[]) => { logged.push(args.map(String).join(' ')); };
  console.error = (...args: unknown[]) => { logged.push(args.map(String).join(' ')); };
  console.warn = (...args: unknown[]) => { logged.push(args.map(String).join(' ')); };
  globalThis.fetch = async () => new Response('one-shot-access-token one-shot-refresh-token one-shot-id-token', { status: 502 });
  try {
    await assert.rejects(
      dispatchProviderOperation(pool!, account.rows[0].id, 'discover_models', {}),
      (error: any) => error.status === 502 && error.code === 'provider_operation_failed' && !String(error.message).includes('one-shot'),
    );
  } finally {
    globalThis.fetch = originalFetch;
    console.log = originalConsole.log;
    console.error = originalConsole.error;
    console.warn = originalConsole.warn;
  }
  assert.equal(logged.join('\n'), '');

  globalThis.fetch = async () => new Response('x'.repeat((2 * 1024 * 1024) + 1), { status: 200 });
  try {
    await assert.rejects(
      dispatchProviderOperation(pool!, account.rows[0].id, 'discover_models', {}),
      (error: any) => error.status === 502 && error.code === 'provider_operation_response_too_large',
    );
  } finally {
    globalThis.fetch = originalFetch;
  }

  globalThis.fetch = async () => new Response(JSON.stringify({ error: { code: 'codex_quota_unsupported', message: 'safe operation error' } }), {
    status: 409,
    headers: { 'content-type': 'application/json' },
  });
  try {
    await assert.rejects(
      dispatchProviderOperation(pool!, account.rows[0].id, 'read_usage', {}),
      (error: any) => error.status === 409 && error.code === 'codex_quota_unsupported' && error.message === 'safe operation error',
    );
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test('API key provider dispatch infers the runtime credential kind', options, async () => {
  const client = await pool!.connect();
  const provider = await client.query("INSERT INTO providers (slug,name,adapter,base_url) VALUES ('api-discovery','API Discovery','openai-compatible','https://api.example.test/v1') RETURNING id");
  const secret = await insertEncrypted(client, { api_key: 'api-discovery-key' });
  const account = await client.query("INSERT INTO accounts (provider_id,secret_record_id,name,display_name,base_url,credential_revision) VALUES ($1,$2,'api-discovery','API Discovery','https://api.example.test/v1',1) RETURNING id", [provider.rows[0].id, secret]);
  client.release();

  const originalFetch = globalThis.fetch;
  let wire: any;
  globalThis.fetch = async (_input, init) => {
    wire = JSON.parse(String(init?.body ?? '{}'));
    return new Response(JSON.stringify({ data: { object: 'list', data: [] } }), {
      status: 200,
      headers: { 'content-type': 'application/json' },
    });
  };
  try {
    const { dispatchProviderOperation } = await import('../lib/provider-operations.ts');
    await dispatchProviderOperation(pool!, account.rows[0].id, 'discover_models', {});
  } finally {
    globalThis.fetch = originalFetch;
  }

  assert.equal(wire.account.adapter, 'openai-compatible');
  assert.equal(wire.account.credential.kind, 'api_key');
  assert.equal(wire.account.credential.api_key, 'api-discovery-key');
  assert.equal(wire.account.credential.revision, 1);
});

test.after(async () => {
  if (pool) {
    await pool.query(`DROP SCHEMA IF EXISTS ${schema} CASCADE`);
    await pool.end();
  }
});
