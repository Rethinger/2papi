import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import { fileURLToPath } from 'node:url';
import { canonicalJson, sha256Canonical } from '../lib/canonical-json.ts';

test('canonical snapshot fixture is byte-stable', async () => {
  const fixture = fileURLToPath(new URL('../../test/fixtures/runtime-snapshot-v2.json', import.meta.url));
  const hashFixture = fileURLToPath(new URL('../../test/fixtures/runtime-snapshot-v2.sha256', import.meta.url));
  const raw = await fs.readFile(fixture, 'utf8');
  const expected = (await fs.readFile(hashFixture, 'utf8')).trim();
  const parsed = JSON.parse(raw);
  assert.equal(canonicalJson(parsed), raw.trim());
  assert.equal(sha256Canonical(parsed), expected);
});
