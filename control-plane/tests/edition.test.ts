import test from 'node:test';
import assert from 'node:assert/strict';
import crypto from 'node:crypto';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { ApiError } from '../lib/api.ts';
import { activeEdition, hasFeature, requireFeature, resetEditionCacheForTests, validateLicense } from '../lib/edition.ts';

const EDITION_ENV = '2PAPI_EDITION';
const PUBKEY_ENV = '2PAPI_LICENSE_PUBKEY';

function makeKeypair() {
  const { publicKey, privateKey } = crypto.generateKeyPairSync('ed25519');
  const spki = publicKey.export({ type: 'spki', format: 'der' }) as Buffer;
  return { pubHex: spki.subarray(spki.length - 32).toString('hex'), privateKey };
}

function signLicense(privateKey: crypto.KeyObject, prefix: string, payload: Record<string, unknown>) {
  const b64payload = Buffer.from(JSON.stringify(payload), 'utf8').toString('base64url');
  const msg = Buffer.from(`${prefix}.${b64payload}`, 'utf8');
  const sig = crypto.sign(null, msg, privateKey);
  return `${prefix}:${b64payload}.${sig.toString('base64url')}`;
}

// Minimal structural type for the test context we use (t.after cleanup).
interface Cleanup {
  after(fn: () => void | Promise<void>): void;
}

function withEnv(t: Cleanup, key: string, value: string | undefined) {
  const old = process.env[key];
  t.after(() => { if (old === undefined) delete process.env[key]; else process.env[key] = old; });
  if (value === undefined) delete process.env[key]; else process.env[key] = value;
}

// Each license-file test gets a fresh cwd + clean gate cache.
function withLicenseDir(t: Cleanup) {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), '2papi-edition-'));
  const originalCwd = process.cwd();
  process.chdir(dir);
  resetEditionCacheForTests();
  t.after(() => {
    process.chdir(originalCwd);
    fs.rmSync(dir, { recursive: true, force: true });
    resetEditionCacheForTests();
  });
  return dir;
}

test('no env and no license degrades to OSS; gated paths refuse', (t) => {
  withEnv(t, EDITION_ENV, undefined);
  assert.equal(activeEdition(), 'oss');
  assert.equal(hasFeature('orgs'), false);
  assert.equal(hasFeature('audit_export'), false);
  assert.throws(() => requireFeature('orgs'), (e: unknown) => e instanceof ApiError && e.status === 403 && e.code === 'feature_not_licensed');
});

test('env override wins: ent unlocks all known features, garbage degrades', async (t) => {
  withEnv(t, EDITION_ENV, undefined);
  assert.equal(activeEdition(), 'oss');
  assert.equal(hasFeature('orgs'), false);

  withEnv(t, EDITION_ENV, 'ent');
  assert.equal(activeEdition(), 'ent');
  assert.equal(hasFeature('orgs'), true);
  assert.equal(hasFeature('sso'), true);

  withEnv(t, EDITION_ENV, 'cloud');
  assert.equal(activeEdition(), 'cloud');

  withEnv(t, EDITION_ENV, 'garbage');
  assert.equal(activeEdition(), 'oss');

  // Unknown features never unlock even under an explicit override.
  withEnv(t, EDITION_ENV, 'ent');
  assert.equal(hasFeature('nonexistent'), false);
});

test('signed ent license unlocks only its feature list', async (t) => {
  withEnv(t, EDITION_ENV, undefined);
  const { pubHex, privateKey } = makeKeypair();
  withEnv(t, PUBKEY_ENV, pubHex);
  withLicenseDir(t);
  const lic = signLicense(privateKey, 'ent', { ed: 'ent', cid: 'acme', cap: 50000, iat: 1, exp: Math.floor(Date.now() / 1000) + 3600, f: ['orgs'] });
  fs.writeFileSync('2papi.license', lic + '\n');
  resetEditionCacheForTests();

  assert.equal(activeEdition(), 'ent');
  assert.equal(hasFeature('orgs'), true);
  assert.equal(hasFeature('sso'), false, 'features not in the license stay locked');

  assert.throws(() => requireFeature('sso'), (e: unknown) => e instanceof ApiError && e.status === 403 && e.code === 'feature_not_licensed');
});

test('expired / not-yet / swapped-prefix / wrong-key licenses degrade to OSS', async (t) => {
  withEnv(t, EDITION_ENV, undefined);
  const { pubHex, privateKey } = makeKeypair();
  withEnv(t, PUBKEY_ENV, pubHex);
  withLicenseDir(t);
  const nowSec = Math.floor(Date.now() / 1000);

  fs.writeFileSync('2papi.license', signLicense(privateKey, 'ent', { ed: 'ent', exp: nowSec - 10 }));
  resetEditionCacheForTests();
  assert.equal(activeEdition(), 'oss', 'expired');

  fs.writeFileSync('2papi.license', signLicense(privateKey, 'ent', { ed: 'ent', exp: nowSec + 3600, nbf: nowSec + 60 }));
  resetEditionCacheForTests();
  assert.equal(activeEdition(), 'oss', 'not valid yet');

  // Prefix says ent, payload signed as cloud — signature check must fail.
  fs.writeFileSync('2papi.license', signLicense(privateKey, 'cloud', { ed: 'cloud', exp: nowSec + 3600 }).replace(/^cloud:/, 'ent:'));
  resetEditionCacheForTests();
  assert.equal(activeEdition(), 'oss', 'swapped prefix breaks the signature');

  const other = makeKeypair();
  fs.writeFileSync('2papi.license', signLicense(other.privateKey, 'ent', { ed: 'ent', exp: nowSec + 3600 }));
  resetEditionCacheForTests();
  assert.equal(activeEdition(), 'oss', 'wrong signing key');
});

test('validateLicense rejects malformed input explicitly', () => {
  assert.throws(() => validateLicense('garbage', 'a'.repeat(64)), /format|key/i);
  assert.throws(() => validateLicense('ent:abc.def', ''), /no trusted public key/i);
  assert.throws(() => validateLicense(`ent:${Buffer.from('{}').toString('base64url')}.aa`, 'zz'), /key/i);
});
