import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import test from 'node:test';
import { ApiError, readJsonBounded, requireGatewayIdentity, requireInternal } from '../lib/api.ts';

const token = 'test-internal-token-123';
function req(auth: string, body = '{}', headers: Record<string,string> = {}) {
  return new Request('http://local', { method: 'POST', body, headers: { authorization: auth, ...headers } });
}

test('requireInternal accepts valid bearer token', () => {
  assert.doesNotThrow(() => requireInternal(req(`Bearer ${token}`), token));
});

test('requireInternal rejects equal, shorter, and longer invalid tokens', () => {
  for (const got of [`Bearer ${'x'.repeat(token.length)}`, 'Bearer short', `Bearer ${token}-extra`]) {
    assert.throws(() => requireInternal(req(got), token), (err) => err instanceof ApiError && err.status === 401);
  }
});

test('requireGatewayIdentity binds the authenticated header to the batch identity', () => {
  const valid = req(`Bearer ${token}`, '{}', { 'x-gateway-id': 'gateway-a' });
  assert.equal(requireGatewayIdentity(valid, 'gateway-a'), 'gateway-a');
  assert.throws(
    () => requireGatewayIdentity(valid, 'gateway-b'),
    (error: any) => error instanceof ApiError && error.status === 403 && error.code === 'gateway_identity_mismatch',
  );
  assert.throws(
    () => requireGatewayIdentity(req(`Bearer ${token}`), 'gateway-a'),
    (error: any) => error instanceof ApiError && error.status === 400 && error.code === 'gateway_identity_missing',
  );
});

test('production runtime rejects known development secrets unless explicitly opted in', () => {
  // Isolated subprocess is required because env.ts validates once at module evaluation.
  const script = "await import('./lib/env.ts')";
  const baseEnv: NodeJS.ProcessEnv = {
    ...process.env,
    NODE_ENV: 'production',
    NEXT_PHASE: '',
    CONTROL_PLANE_MASTER_KEY: 'BwcHBwcHBwcHBwcHBwcHBwcHBwcHBwcHBwcHBwcHBwc=',
    INTERNAL_SERVICE_TOKEN: 'dev-internal-service-token',
    GATEWAY_SHARED_SECRET: 'dev-secret-change-me',
  };
  const rejected = spawnSync(process.execPath, ['--import', 'tsx', '--input-type=module', '--eval', script], {
    cwd: process.cwd(),
    env: { ...baseEnv, ALLOW_INSECURE_DEV_DEFAULTS: '' },
    encoding: 'utf8',
  });
  assert.notEqual(rejected.status, 0, rejected.stdout + rejected.stderr);

  const allowed = spawnSync(process.execPath, ['--import', 'tsx', '--input-type=module', '--eval', script], {
    cwd: process.cwd(),
    env: { ...baseEnv, ALLOW_INSECURE_DEV_DEFAULTS: 'true' },
    encoding: 'utf8',
  });
  assert.equal(allowed.status, 0, allowed.stdout + allowed.stderr);
});

test('readJsonBounded rejects content-length before JSON.parse', async () => {
  const bad = '{"unterminated"';
  await assert.rejects(readJsonBounded(req(`Bearer ${token}`, bad, { 'content-length': '999' }), 8), (err) => err instanceof ApiError && err.status === 413);
});

test('readJsonBounded rejects actual bytes before JSON.parse', async () => {
  const bad = '{"unterminated"';
  await assert.rejects(readJsonBounded(req(`Bearer ${token}`, bad), 8), (err) => err instanceof ApiError && err.status === 413);
});

test('readJsonBounded parses valid bounded JSON', async () => {
  assert.deepEqual(await readJsonBounded<{ ok: boolean }>(req(`Bearer ${token}`, '{"ok":true}'), 32), { ok: true });
});

test('readJsonBounded maps malformed JSON to a 400 API error', async () => {
  await assert.rejects(
    readJsonBounded(req(`Bearer ${token}`, '{"broken":'), 1024),
    (error: any) => error instanceof ApiError && error.status === 400 && error.code === 'invalid_json',
  );
});
