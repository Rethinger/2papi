import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import path from 'node:path';
import { Pool } from 'pg';
import { insertSecret } from '../lib/control.ts';
import { discoverModelsForScope, gatewayDiscoverModels, groupedDiscoveredModels, importSelection, renameModelAlias, validatePublicAlias } from '../lib/codex/operations.ts';
import { sanitizeHistoricalConfigVersions } from '../lib/snapshot-migration.ts';

const url = process.env.TEST_DATABASE_URL;
const options = { skip: url ? false : 'TEST_DATABASE_URL is not set' };
const pool = url ? new Pool({ connectionString: url, max: 4 }) : null;

async function applyMigrations(client: any) {
  const dir = path.join(process.cwd(), 'migrations');
  for (const name of (await fs.readdir(dir)).filter(n => n.endsWith('.sql')).sort()) {
    await client.query(await fs.readFile(path.join(dir, name), 'utf8'));
  }
  await client.query('BEGIN');
  try { await sanitizeHistoricalConfigVersions(client); await client.query('COMMIT'); } catch (e) { await client.query('ROLLBACK'); throw e; }
}

async function withRollback<T>(fn: (client: any) => Promise<T>): Promise<T> {
  const client = await pool!.connect();
  try { await client.query('BEGIN'); return await fn(client); } finally { await client.query('ROLLBACK'); client.release(); }
}

async function seedAccounts(client: any) {
  const provider = await client.query("INSERT INTO providers (slug,name,adapter,base_url) VALUES ('codex-it','Codex IT','codex','https://chatgpt.com') RETURNING id");
  const sid1 = await insertSecret(client, 'codex_account_credential', { access_token: 'tok-1' });
  const sid2 = await insertSecret(client, 'codex_account_credential', { access_token: 'tok-2' });
  const a1 = await client.query("INSERT INTO accounts (provider_id,secret_record_id,name,display_name,base_url,enabled) VALUES ($1,$2,'codex-one','Codex One','https://chatgpt.com',true) RETURNING id", [provider.rows[0].id, sid1]);
  const a2 = await client.query("INSERT INTO accounts (provider_id,secret_record_id,name,display_name,base_url,enabled) VALUES ($1,$2,'codex-two','Codex Two','https://chatgpt.com',true) RETURNING id", [provider.rows[0].id, sid2]);
  await client.query("INSERT INTO routing_settings (id,strategy,sticky_ttl,max_attempts) VALUES (true,'balanced','1h',2) ON CONFLICT (id) DO NOTHING");
  return { providerId: provider.rows[0].id, accountOne: a1.rows[0].id, accountTwo: a2.rows[0].id };
}

test.before(async () => {
  if (!url) return;
  await pool!.query('DROP SCHEMA IF EXISTS public CASCADE; CREATE SCHEMA public;');
  await applyMigrations(pool!);
});

test('discovery persists partial per-account results and marks missing models unavailable', options, async () => {
  await withRollback(async client => {
    const seeded = await seedAccounts(client);
    let call = 0;
    const gateway = async (_account: any) => {
      call++;
      if (call === 2) throw new Error('upstream unavailable');
      return { data: { models: [{ slug: 'luna-code', display_name: 'Luna Code', supported_in_api: true, visibility: 'allow', capabilities: { tools: true }, huge: 'x'.repeat(70000) }] } };
    };
    const first = await discoverModelsForScope(client, { scope: 'all' }, { gatewayOperation: gateway });
    assert.equal(first.scope, 'all');
    assert.equal(first.results.length, 2);
    assert.equal(first.results.filter(r => r.status === 'succeeded').length, 1);
    assert.equal(first.results.filter(r => r.status === 'failed').length, 1);

    const persisted = await client.query('SELECT upstream_model,available,octet_length(raw_metadata::text)::int size FROM discovered_models WHERE account_id=$1', [seeded.accountOne]);
    assert.equal(persisted.rows[0].upstream_model, 'luna-code');
    assert.equal(persisted.rows[0].available, true);
    assert.ok(persisted.rows[0].size <= 32768);

    call = 0;
    const second = await discoverModelsForScope(client, { scope: 'account_id', account_id: seeded.accountOne }, { gatewayOperation: async () => ({ data: { models: [] } }) });
    assert.equal(second.results[0].status, 'succeeded');
    const missing = await client.query('SELECT available FROM discovered_models WHERE account_id=$1 AND upstream_model=$2', [seeded.accountOne, 'luna-code']);
    assert.equal(missing.rows[0].available, false);
  });
});

test('discovery recursively removes credential-bearing raw metadata before bounding and persistence', options, async () => {
  await withRollback(async client => {
    const seeded = await seedAccounts(client);
    const secretValues = [
      'oauth-secret-value',
      'token-secret-value',
      'api-key-secret-value',
      'authorization-secret-value',
      'cookie-secret-value',
      'client-secret-value',
      'password-secret-value',
      'private-key-secret-value',
      'credential-secret-value',
    ];
    await discoverModelsForScope(client, { scope: 'account_id', account_id: seeded.accountOne }, { gatewayOperation: async () => ({ data: { models: [{
      slug: 'secret-filtered-model',
      safeField: 'safe-value',
      oauth: secretValues[0],
      accessToken: secretValues[1],
      API_KEY: secretValues[2],
      Authorization: secretValues[3],
      Cookie: secretValues[4],
      client_secret: secretValues[5],
      password: secretValues[6],
      nested: {
        privateKey: secretValues[7],
        safeNested: 'nested-safe-value',
        array: [{ credential: secretValues[8], visible: 'visible-safe-value' }],
      },
      huge: 'x'.repeat(70000),
    }] } }) });

    const row = await client.query('SELECT raw_metadata, octet_length(raw_metadata::text)::int size FROM discovered_models WHERE account_id=$1 AND upstream_model=$2', [seeded.accountOne, 'secret-filtered-model']);
    const metadataText = JSON.stringify(row.rows[0].raw_metadata);
    assert.ok(row.rows[0].size <= 32768);
    for (const forbidden of ['oauth', 'accessToken', 'API_KEY', 'Authorization', 'Cookie', 'client_secret', 'password', 'privateKey', 'credential', ...secretValues]) {
      assert.equal(metadataText.includes(forbidden), false, `raw_metadata leaked ${forbidden}`);
    }
    assert.equal(metadataText.includes('safeField'), true);
    assert.equal(metadataText.includes('safeNested'), true);
    assert.equal(metadataText.includes('visible-safe-value'), true);
  });
});

test('grouped discovered models aggregate identical slugs for the UI', options, async () => {
  await withRollback(async client => {
    await seedAccounts(client);
    await discoverModelsForScope(client, { scope: 'all' }, { gatewayOperation: async () => ({ data: { models: [{ slug: 'luna-code', supported_in_api: true }] } }) });
    const grouped = await groupedDiscoveredModels(client);
    const luna = grouped.find(g => g.upstream_model === 'luna-code');
    assert.ok(luna);
    assert.equal(luna!.account_count, 2);
    assert.equal(luna!.available_account_count, 2);
  });
});

test('grouped discovered models keep identical upstream names isolated by provider', options, async () => {
  await withRollback(async client => {
    const first = await seedAccounts(client);
    const otherProvider = await client.query("INSERT INTO providers (slug,name,adapter,base_url) VALUES ('api-it','API IT','openai-compatible','https://api.example.test/v1') RETURNING id");
    const otherSecret = await insertSecret(client, 'api_key', { api_key: 'safe-test-key' });
    const otherAccount = await client.query("INSERT INTO accounts (provider_id,secret_record_id,name,display_name,base_url,enabled) VALUES ($1,$2,'api-one','API One','https://api.example.test/v1',true) RETURNING id", [otherProvider.rows[0].id, otherSecret]);
    await discoverModelsForScope(client, { scope: 'account_id', account_id: first.accountOne }, { gatewayOperation: async () => ({ data: { models: [{ slug: 'gpt-5.6-luna', context_window: 272000, supported_in_api: true }] } }) });
    await discoverModelsForScope(client, { scope: 'account_id', account_id: otherAccount.rows[0].id }, { gatewayOperation: async () => ({ data: { data: [{ id: 'gpt-5.6-luna', tool_call: true, owned_by: 'api-owner' }] } }) });

    const luna = (await groupedDiscoveredModels(client)).filter(model => model.upstream_model === 'gpt-5.6-luna');
    assert.equal(luna.length, 2);
    assert.deepEqual(luna.map(model => model.provider_id).sort(), [first.providerId, otherProvider.rows[0].id].sort());
    assert.deepEqual(Object.keys(luna.find(model => model.provider_id === first.providerId).accounts), [first.accountOne]);
    assert.deepEqual(Object.keys(luna.find(model => model.provider_id === otherProvider.rows[0].id).accounts), [otherAccount.rows[0].id]);
    assert.equal(luna.find(model => model.provider_id === first.providerId).metadata.context_window, 272000);
    assert.equal(luna.find(model => model.provider_id === otherProvider.rows[0].id).metadata.tools, true);
  });
});

test('discovery persists models from the OpenAI-compatible data envelope', options, async () => {
  await withRollback(async client => {
    const seeded = await seedAccounts(client);
    const result = await discoverModelsForScope(client, { scope: 'account_id', account_id: seeded.accountOne }, {
      gatewayOperation: async () => ({ data: { object: 'list', data: [{ id: 'gpt-api-model', owned_by: 'provider' }] } }),
    });

    assert.equal(result.results[0].status, 'succeeded');
    assert.equal(result.results[0].model_count, 1);
    const stored = await client.query('SELECT upstream_model,display_name,available FROM discovered_models WHERE account_id=$1', [seeded.accountOne]);
    assert.deepEqual(stored.rows.map((row: any) => row.upstream_model), ['gpt-api-model']);
    assert.equal(stored.rows[0].display_name, 'gpt-api-model');
    assert.equal(stored.rows[0].available, true);
  });
});

test('discovery scopes provider_id and account_id, caps concurrency at four, and returns safe partial errors', options, async () => {
  await withRollback(async client => {
    const seeded = await seedAccounts(client);
    const otherProvider = await client.query("INSERT INTO providers (slug,name,adapter,base_url) VALUES ('other-codex','Other Codex','codex','https://chatgpt.com') RETURNING id");
    const otherSecret = await insertSecret(client, 'codex_account_credential', { access_token: 'secret-other' });
    await client.query("INSERT INTO accounts (provider_id,secret_record_id,name,display_name,base_url,enabled) VALUES ($1,$2,'other-one','Other One','https://chatgpt.com',true)", [otherProvider.rows[0].id, otherSecret]);

    const providerSeen: string[] = [];
    await discoverModelsForScope(client, { scope: 'provider_id', provider_id: seeded.providerId }, { gatewayOperation: async account => { providerSeen.push(account.id); return { data: { models: [{ slug: 'scoped-model' }] } }; } });
    assert.deepEqual(providerSeen.sort(), [seeded.accountOne, seeded.accountTwo].sort());

    const accountSeen: string[] = [];
    await discoverModelsForScope(client, { scope: 'account_id', account_id: seeded.accountOne }, { gatewayOperation: async account => { accountSeen.push(account.id); return { data: { models: [{ slug: 'one-model' }] } }; } });
    assert.deepEqual(accountSeen, [seeded.accountOne]);

    for (let i = 0; i < 7; i++) {
      const sid = await insertSecret(client, 'codex_account_credential', { access_token: `secret-${i}` });
      await client.query("INSERT INTO accounts (provider_id,secret_record_id,name,display_name,base_url,enabled) VALUES ($1,$2,$3,$4,'https://chatgpt.com',true)", [seeded.providerId, sid, `bulk-${i}`, `Bulk ${i}`]);
    }
    let active = 0;
    let maxActive = 0;
    const result = await discoverModelsForScope(client, { scope: 'provider_id', provider_id: seeded.providerId }, { gatewayOperation: async account => {
      active++;
      maxActive = Math.max(maxActive, active);
      await new Promise(resolve => setTimeout(resolve, 10));
      active--;
      if (account.id === seeded.accountTwo) throw new Error('token secret-should-not-leak response body {"access_token":"abc"}');
      return { data: { models: [{ slug: 'safe-model' }] } };
    } });
    assert.ok(maxActive <= 4, `expected <=4 concurrent gateway calls, got ${maxActive}`);
    const failed = result.results.find(r => r.status === 'failed');
    assert.ok(failed && failed.status === 'failed');
    assert.equal(JSON.stringify(failed).includes('secret-should-not-leak'), false);
    assert.equal(JSON.stringify(failed).includes('access_token'), false);
  });
});
test('gateway discovery client sends the strict provider-operation contract with runtime account credential', options, async () => {
  await withRollback(async client => {
    const seeded = await seedAccounts(client);
    const account = (await client.query('SELECT a.id,a.provider_id,a.name,a.display_name,a.base_url,a.enabled,p.adapter FROM accounts a JOIN providers p ON p.id=a.provider_id WHERE a.id=$1', [seeded.accountOne])).rows[0];
    const originalFetch = globalThis.fetch;
    let body: any;
    globalThis.fetch = (async (_url: any, init: any) => {
      body = JSON.parse(init.body);
      return new Response(JSON.stringify({ data: { models: [{ slug: 'wire-model' }] } }), { status: 200, headers: { 'content-type': 'application/json' } });
    }) as any;
    try {
      await gatewayDiscoverModels(account, client);
    } finally {
      globalThis.fetch = originalFetch;
    }
    assert.equal(body.operation, 'discover_models');
    assert.equal(body.operation_type, undefined);
    assert.equal(body.account_id, undefined);
    assert.equal(body.account.id, seeded.accountOne);
    assert.equal(body.account.credential.access_token, 'tok-1');
    assert.equal(typeof body.idempotency_key, 'string');
  });
});

test('discovery rejects malformed model entries before persistence', options, async () => {
  await withRollback(async client => {
    const seeded = await seedAccounts(client);
    const result = await discoverModelsForScope(client, { scope: 'account_id', account_id: seeded.accountOne }, { gatewayOperation: async () => ({ data: { models: [{ display_name: 'Missing slug' }] } }) });
    assert.equal(result.results[0].status, 'failed');
    const rows = await client.query('SELECT count(*)::int n FROM discovered_models WHERE account_id=$1', [seeded.accountOne]);
    assert.equal(rows.rows[0].n, 0);
  });
});
test('alias validation is exact, case-insensitive, and rejects whitespace/control/trailing separators', options, async () => {
  await withRollback(async client => {
    assert.equal(validatePublicAlias('luna-code'), 'luna-code');
    await client.query("INSERT INTO model_aliases (alias,upstream_model) VALUES ('luna-code','luna-code')");
    await assert.rejects(async () => validatePublicAlias('bad alias'), { code: 'invalid_model_alias' });
    await assert.rejects(async () => validatePublicAlias('bad\n'), { code: 'invalid_model_alias' });
    await assert.rejects(async () => validatePublicAlias('luna-code/'), { code: 'invalid_model_alias' });
    await assert.rejects(() => importSelection(client, { alias: 'Luna-Code', upstream_model: 'luna-code', account_ids: [] }), { code: 'model_alias_conflict' });
  });
});

test('import selection creates a draft alias and mappings without publishing', options, async () => {
  await withRollback(async client => {
    const seeded = await seedAccounts(client);
    await client.query("INSERT INTO virtual_keys (name,key_hash,key_prefix,models,rpm) VALUES ('import-vk',$1,'sk-import',ARRAY[]::text[],60)", ['d'.repeat(64)]);
    await discoverModelsForScope(client, { scope: 'account_id', account_id: seeded.accountOne }, { gatewayOperation: async () => ({ data: { models: [{ slug: 'luna-code' }] } }) });
    const imported = await importSelection(client, { alias: 'luna-code', upstream_model: 'luna-code', account_ids: [seeded.accountOne] });
    assert.equal(imported.alias, 'luna-code');
    const drafts = await client.query("SELECT count(*)::int n FROM config_versions WHERE status='draft'");
    const pubs = await client.query("SELECT count(*)::int n FROM config_versions WHERE status='published'");
    assert.equal(drafts.rows[0].n, 1);
    assert.equal(pubs.rows[0].n, 0);
  });
});

test('rename updates virtual key model references transactionally and rolls back on dependent failure', options, async () => {
  await withRollback(async client => {
    const seeded = await seedAccounts(client);
    const model = await client.query("INSERT INTO model_aliases (alias,upstream_model) VALUES ('luna-code','luna-code') RETURNING id");
    await client.query('INSERT INTO model_account_mappings (model_alias_id,account_id) VALUES ($1,$2)', [model.rows[0].id, seeded.accountOne]);
    await client.query("INSERT INTO virtual_keys (name,key_hash,key_prefix,models,rpm) VALUES ('vk',$1,'sk-vk',ARRAY['luna-code','other'],60)", ['c'.repeat(64)]);

    await client.query('SAVEPOINT before_failed_rename');
    await assert.rejects(() => renameModelAlias(client, model.rows[0].id, 'nova-code', { afterAliasUpdate: async () => { throw new Error('dependent update failed'); } }), /dependent update failed/);
    await client.query('ROLLBACK TO SAVEPOINT before_failed_rename');
    const rolledAlias = await client.query('SELECT alias FROM model_aliases WHERE id=$1', [model.rows[0].id]);
    const rolledVk = await client.query("SELECT models FROM virtual_keys WHERE name='vk'");
    assert.equal(rolledAlias.rows[0].alias, 'luna-code');
    assert.deepEqual(rolledVk.rows[0].models, ['luna-code', 'other']);

    await renameModelAlias(client, model.rows[0].id, 'nova-code');
    const vk = await client.query("SELECT models FROM virtual_keys WHERE name='vk'");
    assert.deepEqual(vk.rows[0].models, ['nova-code', 'other']);
  });
});

test.after(async () => { await pool?.end(); });
