// Mirror of internal/proxylib (Go) — keeps the control plane able to
// validate proxy lists (and show per-line errors) with the same rules as
// the gateway. The Go parser is authoritative for runtime behavior; keep the
// test tables in proxylib.test.ts in sync with internal/proxylib tests.
//
// Accepted formats (mixed freely; newline/comma/semicolon/whitespace
// separated; a JSON array of strings is accepted too):
//   http://user:pass@host:8080   https://host:443
//   socks5://host:1080           socks5h://user:pass@host
//   socks4://host:1080           socks4a://host:1080
//   user:pass@host:8080          host:8080
//   host:8080:user:pass          host               (http, port 80)
//   [::1]:8080                   [::1]:8080:user:pass
//
// Rules: scheme defaults to http (ports: http 80, https 443, socks* 1080);
// path/query/fragment stripped; IPv6 must be bracketed; '#' comments;
// duplicates removed; passwords never exposed by maskProxy().

export type ProxyScheme = 'http' | 'https' | 'socks4' | 'socks4a' | 'socks5' | 'socks5h';

export type ProxyEntry = {
  scheme: ProxyScheme;
  host: string;
  port: number;
  user: string;
  pass: string;
};

export type ProxyLineError = { line: number; text: string; reason: string };

const KNOWN_SCHEMES: ReadonlySet<string> = new Set(['http', 'https', 'socks4', 'socks4a', 'socks5', 'socks5h']);

function defaultPort(scheme: string): number {
  if (scheme === 'http') return 80;
  if (scheme === 'https') return 443;
  return 1080;
}

function validateHost(host: string): string | null {
  if (!host) return 'missing proxy host';
  if (/[\s/\\@]/.test(host)) return `invalid proxy host "${host}"`;
  // Hostname chars or an IPv6 literal (colons/brackets allowed inside).
  if (!/^[A-Za-z0-9._:\-\[\]]+$/.test(host)) return `invalid character in proxy host "${host}"`;
  return null;
}

function validateCreds(user: string, pass: string): string | null {
  for (const s of [user, pass]) {
    if (/[\x00-\x1f\x7f]/.test(s)) return 'proxy credentials contain control characters';
  }
  return null;
}

function portOf(raw: string | undefined, scheme: string): number | string {
  if (!raw) return defaultPort(scheme);
  const p = Number(raw);
  if (!Number.isInteger(p) || p < 1 || p > 65535) return `invalid proxy port "${raw}"`;
  return p;
}

function parseSchemeUrl(token: string): ProxyEntry | string {
  const idx = token.indexOf('://');
  const scheme = token.slice(0, idx).toLowerCase();
  if (!KNOWN_SCHEMES.has(scheme)) return `unsupported proxy scheme "${scheme}"`;
  let u: URL;
  try {
    // URL normalizes userinfo/host; path/query/fragment are stripped below.
    u = new URL(token);
  } catch {
    return 'malformed proxy URL';
  }
  if (!u.hostname) return 'missing proxy host';
  const port = portOf(u.port || undefined, scheme);
  if (typeof port === 'string') return port;
  const user = u.username ? decodeURIComponent(u.username) : '';
  const pass = u.password ? decodeURIComponent(u.password) : '';
  const credErr = validateCreds(user, pass);
  if (credErr) return credErr;
  return { scheme: scheme as ProxyScheme, host: u.hostname, port, user, pass };
}

function parseBare(token: string): ProxyEntry | string {
  let user = '';
  let pass = '';
  let rest = token;

  const at = rest.lastIndexOf('@');
  if (at >= 0) {
    const userinfo = rest.slice(0, at);
    rest = rest.slice(at + 1);
    if (!userinfo) return 'empty proxy credentials';
    const sep = userinfo.indexOf(':');
    if (sep < 0) user = userinfo;
    else {
      user = userinfo.slice(0, sep);
      pass = userinfo.slice(sep + 1);
    }
    if (!user && !pass) return 'empty proxy credentials';
  }

  let host = '';
  let portStr = '';
  if (!user && !pass && !rest.startsWith('[') && (rest.match(/:/g) ?? []).length === 3) {
    // host:port:user:pass
    const parts = rest.split(':');
    host = parts[0];
    portStr = parts[1];
    user = parts[2];
    pass = parts[3];
  } else if (rest.startsWith('[')) {
    const end = rest.indexOf(']');
    if (end < 0) return 'missing proxy host';
    host = rest.slice(1, end);
    const tail = rest.slice(end + 1);
    if (tail) {
      if (!tail.startsWith(':')) return 'missing proxy host';
      const parts = tail.slice(1).split(':');
      portStr = parts[0] ?? '';
      if (parts.length === 3 && !user) {
        user = parts[1];
        pass = parts[2];
      }
    }
  } else {
    const parts = rest.split(':');
    if (parts.length === 1) {
      host = parts[0];
    } else if (parts.length === 2) {
      host = parts[0];
      portStr = parts[1];
    } else {
      return 'missing proxy host';
    }
  }

  if (!host) return 'missing proxy host';
  const port = portOf(portStr || undefined, 'http');
  if (typeof port === 'string') return port;
  const hostErr = validateHost(host);
  if (hostErr) return hostErr;
  const credErr = validateCreds(user, pass);
  if (credErr) return credErr;
  return { scheme: 'http', host, port, user, pass };
}

export function parseProxyEntry(token: string): ProxyEntry | string {
  const trimmed = token.trim();
  if (!trimmed) return 'empty proxy entry';
  if (trimmed.includes('://')) return parseSchemeUrl(trimmed);
  return parseBare(trimmed);
}

function addEntry(
  token: string,
  lineNo: number,
  entries: ProxyEntry[],
  errs: ProxyLineError[],
  seen: Set<string>,
): void {
  const parsed = parseProxyEntry(token);
  if (typeof parsed === 'string') {
    errs.push({ line: lineNo, text: token.trim(), reason: parsed });
    return;
  }
  const key = `${parsed.scheme}://${parsed.user}:${parsed.pass}@${parsed.host}:${parsed.port}`;
  if (seen.has(key)) return;
  seen.add(key);
  entries.push(parsed);
}

export function parseProxyList(raw: string): { entries: ProxyEntry[]; errors: ProxyLineError[] } {
  const entries: ProxyEntry[] = [];
  const errors: ProxyLineError[] = [];
  const seen = new Set<string>();
  const trimmed = raw.trim();

  // Whole input is a JSON array of strings.
  if (trimmed.startsWith('[')) {
    try {
      const list = JSON.parse(trimmed) as unknown;
      if (Array.isArray(list) && list.every(item => typeof item === 'string')) {
        (list as string[]).forEach((item, i) => addEntry(item, i + 1, entries, errors, seen));
        return { entries, errors };
      }
    } catch {
      // fall through to line parsing
    }
  }

  raw.split('\n').forEach((rawLine, lineIndex) => {
    const lineNo = lineIndex + 1;
    let line = rawLine.trim();
    if (!line || line.startsWith('#')) return;
    const hash = line.indexOf('#');
    if (hash >= 0) {
      line = line.slice(0, hash).trim();
      if (!line) return;
    }
    // A line that is itself a JSON array of strings.
    if (line.startsWith('[') && line.endsWith(']')) {
      try {
        const list = JSON.parse(line) as unknown;
        if (Array.isArray(list) && list.every(item => typeof item === 'string')) {
          (list as string[]).forEach(item => addEntry(item, lineNo, entries, errors, seen));
          return;
        }
      } catch {
        // fall through to token splitting
      }
    }
    for (const token of line.split(/[,\s;]+/)) {
      if (token) addEntry(token, lineNo, entries, errors, seen);
    }
  });
  return { entries, errors };
}

// maskProxy returns a log/UI-safe representation: user:****@host:port.
export function maskProxy(entry: ProxyEntry): string {
  const hostport = entry.host.includes(':') ? `[${entry.host}]:${entry.port}` : `${entry.host}:${entry.port}`;
  if (!entry.user) return `${entry.scheme}://${hostport}`;
  return `${entry.scheme}://${entry.user}:****@${hostport}`;
}

// normalizeProxy returns the canonical scheme://user:pass@host:port string
// the gateway stores/publishes (passwords included — never log it).
export function normalizeProxy(entry: ProxyEntry): string {
  const hostport = entry.host.includes(':') ? `[${entry.host}]:${entry.port}` : `${entry.host}:${entry.port}`;
  const creds = entry.user ? `${entry.user}:${entry.pass}@` : '';
  return `${entry.scheme}://${creds}${hostport}`;
}
