import test from 'node:test';
import assert from 'node:assert/strict';
import { rateLimit, clearRateLimitsForTests } from '../lib/rate-limit.ts';

test('fixed window allows up to the limit then blocks and resets', (t) => {
  t.after(() => clearRateLimitsForTests());
  const key = 'ip-a';
  let now = 1_000_000;
  const realNow = Date.now;
  Date.now = () => now;
  t.after(() => { Date.now = realNow; });

  for (let i = 0; i < 5; i++) {
    assert.equal(rateLimit(key, 5, 60_000), true, `attempt ${i + 1} allowed`);
  }
  assert.equal(rateLimit(key, 5, 60_000), false, '6th attempt in window blocked');

  now += 61_000; // window passed
  assert.equal(rateLimit(key, 5, 60_000), true, 'fresh window allows again');
});

test('keys are isolated from each other', () => {
  clearRateLimitsForTests();
  assert.equal(rateLimit('signup:1.1.1.1', 1, 60_000), true);
  assert.equal(rateLimit('signup:1.1.1.1', 1, 60_000), false);
  assert.equal(rateLimit('login:1.1.1.1', 1, 60_000), true, 'different route bucket');
  assert.equal(rateLimit('signup:2.2.2.2', 1, 60_000), true, 'different ip');
});

test('zero limit disables the endpoint entirely', () => {
  clearRateLimitsForTests();
  assert.equal(rateLimit('off', 0, 60_000), false);
});
