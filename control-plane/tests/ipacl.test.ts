import test from 'node:test';
import assert from 'node:assert/strict';
import { parseCidrs, ipAllowed } from '../lib/ipacl.ts';

test('parseCidrs accepts IPv4 CIDRs and literal IPs, rejects garbage', () => {
  assert.deepEqual(parseCidrs(['10.0.0.0/8', '192.168.1.1/32', '203.0.113.7']), ['10.0.0.0/8', '192.168.1.1/32', '203.0.113.7']);
  assert.deepEqual(parseCidrs(['2001:db8::1']), ['2001:db8::1']); // IPv6 literal kept for exact match
  for (const bad of [['999.0.0.0/8'], ['10.0.0.0/33'], ['10.0.0.0/-1'], ['abc'], [42], ['10.0.0.0/8/x'], [{}]]) {
    assert.throws(() => parseCidrs(bad), `should reject ${JSON.stringify(bad)}`);
  }
  assert.throws(() => parseCidrs('not-an-array'));
});

test('ipAllowed matches by CIDR range with host bits ignored', () => {
  const cidrs = ['10.0.0.0/8', '192.168.1.1/32'];
  assert.equal(ipAllowed('10.255.255.255', cidrs), true);
  assert.equal(ipAllowed('10.0.0.0', cidrs), true);
  assert.equal(ipAllowed('11.0.0.0', cidrs), false);
  assert.equal(ipAllowed('192.168.1.1', cidrs), true);
  assert.equal(ipAllowed('192.168.1.2', cidrs), false);
  // host bits in the CIDR itself are ignored: 10.1.2.99/16 == 10.1.0.0/16
  assert.equal(ipAllowed('10.1.200.5', ['10.1.2.99/16']), true);
  assert.equal(ipAllowed('10.2.0.1', ['10.1.2.99/16']), false);
  // /0 admits everything
  assert.equal(ipAllowed('8.8.8.8', ['0.0.0.0/0']), true);
});

test('ipAllowed falls back to exact match for IPv6 and empty list allows all', () => {
  assert.equal(ipAllowed('2001:db8::1', ['2001:db8::1']), true);
  assert.equal(ipAllowed('2001:db8::2', ['2001:db8::1']), false);
  assert.equal(ipAllowed('1.2.3.4', []), true);
});
