import { ApiError } from './api';
import { hasFeature } from './edition';

// IP ACL (Enterprise feature 'ipacl'): an operator-configured allowlist of
// IPv4 CIDRs applied to requests that arrive THROUGH a proxy (X-Forwarded-For).
// Requests without XFF hit the control plane directly (local dashboard /
// loopback tooling) and always pass — the ACL governs remote edge traffic,
// and keying on a header we cannot have locally avoids the lockout footgun.

export function parseCidrs(raw: unknown): string[] {
  if (!Array.isArray(raw)) throw new ApiError(400, 'invalid_cidrs', 'cidrs must be an array of strings');
  const out: string[] = [];
  for (const item of raw) {
    if (typeof item !== 'string' || !isCidr(item)) {
      throw new ApiError(400, 'invalid_cidrs', `Not a valid IPv4 CIDR or literal IP: ${String(item)}`);
    }
    out.push(item);
  }
  return out;
}

function isCidr(value: string): boolean {
  if (value.includes(':')) return value.length > 0 && value.length <= 45; // IPv6 literal, exact match only
  const segments = value.split('/');
  if (segments.length > 2) return false;
  const [ip, bitsRaw] = segments;
  if (bitsRaw !== undefined && !/^\d{1,2}$/.test(bitsRaw)) return false;
  const bits = bitsRaw === undefined ? 32 : Number(bitsRaw);
  if (bits < 0 || bits > 32) return false;
  const parts = ip.split('.');
  if (parts.length !== 4) return false;
  return parts.every(part => /^\d{1,3}$/.test(part) && Number(part) <= 255);
}

function v4ToNumber(ip: string): number {
  let value = 0;
  for (const part of ip.split('.')) value = value * 256 + Number(part);
  return value;
}

function v4Range(cidr: string): { lo: number; hi: number } | null {
  if (cidr.includes(':')) return null;
  const segments = cidr.split('/');
  if (segments.length > 2) return null;
  const [ip, bitsRaw] = segments;
  const bits = bitsRaw === undefined ? 32 : Number(bitsRaw);
  const base = v4ToNumber(ip);
  const size = 2 ** (32 - bits);
  const lo = Math.floor(base / size) * size; // host bits are ignored, like inet_aton semantics
  return { lo, hi: lo + size - 1 };
}

export function ipAllowed(ip: string, cidrs: string[]): boolean {
  if (cidrs.length === 0) return true;
  if (!/^(\d{1,3}\.){3}\d{1,3}$/.test(ip)) return cidrs.includes(ip); // IPv6/odd: exact match
  const value = v4ToNumber(ip);
  return cidrs.some(cidr => {
    const range = v4Range(cidr);
    return range !== null && value >= range.lo && value <= range.hi;
  });
}

// assertIpacl throws when the request's proxy-reported client IP falls
// outside the configured allowlist. No-op in OSS or when no list is set.
export async function assertIpacl(req: Request): Promise<void> {
  if (!hasFeature('ipacl')) return;
  const forwarded = req.headers.get('x-forwarded-for');
  if (!forwarded) return; // direct/loopback access bypasses by design (see header comment)
  const { pool } = await import('./db');
  const q = await pool.query(`SELECT value FROM system_settings WHERE key='ipacl'`);
  const raw = q.rows[0]?.value as { cidrs?: unknown } | undefined;
  const cidrs = Array.isArray(raw?.cidrs) ? (raw!.cidrs as string[]) : [];
  if (cidrs.length === 0) return;
  const ip = forwarded.split(',')[0].trim();
  if (!ipAllowed(ip, cidrs)) {
    throw new ApiError(403, 'ip_not_allowed', `Client IP ${ip} is not in the configured allowlist`);
  }
}
