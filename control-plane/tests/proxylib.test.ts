import test from 'node:test';
import assert from 'node:assert/strict';
import { maskProxy, normalizeProxy, parseProxyEntry, parseProxyList } from '../lib/proxylib.ts';

// Mirror of internal/proxylib Go test tables — keep in sync.

test('parseProxyEntry accepts every supported format', () => {
  const cases: Array<[string, { scheme: string; host: string; port: number; user: string; pass: string }]> = [
    ['http://user:pass@host:8080', { scheme: 'http', host: 'host', port: 8080, user: 'user', pass: 'pass' }],
    ['https://host:443', { scheme: 'https', host: 'host', port: 443, user: '', pass: '' }],
    ['https://host', { scheme: 'https', host: 'host', port: 443, user: '', pass: '' }],
    ['socks5://host:1080', { scheme: 'socks5', host: 'host', port: 1080, user: '', pass: '' }],
    ['socks5://host', { scheme: 'socks5', host: 'host', port: 1080, user: '', pass: '' }],
    ['socks5h://user:pass@host', { scheme: 'socks5h', host: 'host', port: 1080, user: 'user', pass: 'pass' }],
    ['socks4://host:1080', { scheme: 'socks4', host: 'host', port: 1080, user: '', pass: '' }],
    ['socks4a://host:1080', { scheme: 'socks4a', host: 'host', port: 1080, user: '', pass: '' }],
    ['user:pass@host:8080', { scheme: 'http', host: 'host', port: 8080, user: 'user', pass: 'pass' }],
    ['host:8080', { scheme: 'http', host: 'host', port: 8080, user: '', pass: '' }],
    ['host', { scheme: 'http', host: 'host', port: 80, user: '', pass: '' }],
    ['host:8080:user:pass', { scheme: 'http', host: 'host', port: 8080, user: 'user', pass: 'pass' }],
    ['10.0.0.1:3128', { scheme: 'http', host: '10.0.0.1', port: 3128, user: '', pass: '' }],
    ['[::1]:8080', { scheme: 'http', host: '::1', port: 8080, user: '', pass: '' }],
    ['[2001:db8::1]:8080:user:pass', { scheme: 'http', host: '2001:db8::1', port: 8080, user: 'user', pass: 'pass' }],
    ['http://host:8080/path?query#frag', { scheme: 'http', host: 'host', port: 8080, user: '', pass: '' }],
    ['http://user:pass@host:8080/path', { scheme: 'http', host: 'host', port: 8080, user: 'user', pass: 'pass' }],
  ];
  for (const [input, want] of cases) {
    const parsed = parseProxyEntry(input);
    assert.notEqual(typeof parsed, 'string', `${input} should parse`);
    assert.deepEqual(parsed, want, input);
  }
});

test('parseProxyEntry rejects invalid input', () => {
  const invalid = [
    '',
    '   ',
    'not a proxy!!',
    'host:99999',
    'host:0',
    'http://',
    'http://:8080',
    'ftp://host:21',
    'socks6://host:1080',
    'user:pass@',
    '@host:8080',
    '2001:db8::1',
    '[::1',
    'ho st:8080',
  ];
  for (const input of invalid) {
    const parsed = parseProxyEntry(input);
    assert.equal(typeof parsed, 'string', `${JSON.stringify(input)} should fail`);
  }
});

test('parseProxyList handles mixed formats, comments, JSON and dedupe', () => {
  const raw = `# global pool
http://user:pass@host-a:8080
host-b:3128
socks5://host-c:1080, socks4a://host-d:1080; user:p@host-e:8080
host-f:8080:u1:p1 # inline comment
[::1]:9090
["http://json-a:1", "socks5://json-b:2"]`;
  const { entries, errors } = parseProxyList(raw);
  assert.deepEqual(errors, []);
  assert.deepEqual(entries.map(e => `${e.scheme}://${e.host}:${e.port}`), [
    'http://host-a:8080',
    'http://host-b:3128',
    'socks5://host-c:1080',
    'socks4a://host-d:1080',
    'http://host-e:8080',
    'http://host-f:8080',
    'http://::1:9090',
    'http://json-a:1',
    'socks5://json-b:2',
  ]);
});

test('parseProxyList reports line errors and keeps valid entries', () => {
  const { entries, errors } = parseProxyList('host-a:1\nhost-b:99999\nhost-c:2');
  assert.equal(entries.length, 2);
  assert.equal(errors.length, 1);
  assert.equal(errors[0].line, 2);
  assert.match(errors[0].reason, /port/);
});

test('parseProxyList dedupes', () => {
  const { entries } = parseProxyList('host-a:1\nhost-a:1\nhost-a:1:u:p\nhost-b:2');
  assert.equal(entries.length, 3);
});

test('parseProxyList keeps rotating-session passwords as distinct proxies', () => {
  const { entries, errors } = parseProxyList('socks5://u:p_session-AAA@host:2002\nsocks5://u:p_session-BBB@host:2002\nsocks5://u:p_session-AAA@host:2002');
  assert.deepEqual(errors, []);
  assert.equal(entries.length, 2);
  assert.notEqual(entries[0].pass, entries[1].pass);
});

test('whole-input JSON array parses', () => {
  const { entries, errors } = parseProxyList('["http://a:1", "socks5://b:2"]');
  assert.deepEqual(errors, []);
  assert.equal(entries.length, 2);
  assert.equal(entries[0].scheme, 'http');
  assert.equal(entries[1].scheme, 'socks5');
});

test('maskProxy never leaks passwords', () => {
  for (const raw of ['http://user:secret@host:8080', 'user:secret@host:8080', 'host:8080:user:secret', 'socks5://user:secret@host']) {
    const parsed = parseProxyEntry(raw);
    if (typeof parsed === 'string') throw new Error(`unexpected parse failure: ${raw}`);
    const masked = maskProxy(parsed);
    assert.ok(!masked.includes('secret'), `${raw} leaked: ${masked}`);
    assert.ok(masked.includes(':****@'), `${raw} not masked: ${masked}`);
  }
  assert.equal(maskProxy({ scheme: 'http', host: 'host', port: 8080, user: '', pass: '' }), 'http://host:8080');
});

test('normalizeProxy round-trips into the gateway canonical format', () => {
  const raw = 'host:8080:user:pass';
  const parsed = parseProxyEntry(raw);
  if (typeof parsed === 'string') throw new Error('unexpected');
  assert.equal(normalizeProxy(parsed), 'http://user:pass@host:8080');
  const v6 = parseProxyEntry('[::1]:9090');
  if (typeof v6 === 'string') throw new Error('unexpected');
  assert.equal(normalizeProxy(v6), 'http://[::1]:9090');
});
