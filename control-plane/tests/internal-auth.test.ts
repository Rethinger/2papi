import assert from 'node:assert/strict';
import test from 'node:test';
import { ApiError, readJsonBounded, requireInternal } from '../lib/api.ts';

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
