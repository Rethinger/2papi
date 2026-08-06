import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import { fileURLToPath } from 'node:url';
import { canonicalJson, sha256Canonical } from '../lib/canonical-json.ts';

test('canonical snapshot fixture is byte-stable', async () => {
  const rootFixture = fileURLToPath(new URL('../../test/fixtures/runtime-snapshot-v2.json', import.meta.url));
  const rootHashFixture = fileURLToPath(new URL('../../test/fixtures/runtime-snapshot-v2.sha256', import.meta.url));
  const localFixture = fileURLToPath(new URL('./fixtures/runtime-snapshot-v2.json', import.meta.url));
  const localHashFixture = fileURLToPath(new URL('./fixtures/runtime-snapshot-v2.sha256', import.meta.url));
  const rootExists = await exists(rootFixture);
  const localExists = await exists(localFixture);
  assert.ok(rootExists || localExists, 'runtime snapshot v2 fixture missing');
  if (rootExists && localExists) assert.equal(await fs.readFile(localFixture, 'utf8'), await fs.readFile(rootFixture, 'utf8'));
  if (rootExists && await exists(localHashFixture)) assert.equal(await fs.readFile(localHashFixture, 'utf8'), await fs.readFile(rootHashFixture, 'utf8'));
  const fixture = rootExists ? rootFixture : localFixture;
  const hashFixture = rootExists ? rootHashFixture : localHashFixture;
  const raw = await fs.readFile(fixture, 'utf8');
  const expected = (await fs.readFile(hashFixture, 'utf8')).trim();
  const parsed = JSON.parse(raw);
  assert.equal(canonicalJson(parsed), raw.trim());
  assert.equal(sha256Canonical(parsed), expected);
});

async function exists(path: string) { try { await fs.access(path); return true; } catch { return false; } }
