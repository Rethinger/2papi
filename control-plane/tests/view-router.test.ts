import test from 'node:test';
import assert from 'node:assert/strict';
import { pathForView, viewFromPath, type View } from '../app/view-router.ts';

test('view paths round-trip through the router', () => {
  for (const view of ['overview', 'requests', 'accounts', 'models', 'keys', 'teams', 'audit', 'settings'] as View[]) {
    assert.equal(viewFromPath(pathForView(view)), view);
  }
});

test('unknown and root paths resolve to overview', () => {
  assert.equal(viewFromPath('/'), 'overview');
  assert.equal(viewFromPath(''), 'overview');
  assert.equal(viewFromPath('/nonsense'), 'overview');
  assert.equal(viewFromPath('/models?x=1'), 'models');
  assert.equal(viewFromPath('/keys/'), 'keys');
  assert.equal(viewFromPath('/teams#section'), 'teams');
});
