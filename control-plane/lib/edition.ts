import crypto from 'node:crypto';
import fs from 'node:fs';
import { ApiError } from './api';

// Edition gate for the control-plane — mirrors internal/edition +
// internal/license on the Go side (one binary, three editions; spec:
// docs/build-spine-specs.md шаг 1). Enterprise features "sleep" without a
// license: every check degrades to OSS.
//
// Detection order:
//   1. env 2PAPI_EDITION ("oss"|"cloud"|"ent") — wins always; an explicit
//      operator override unlocks all enterprise features (tests, air-gap
//      deployments that manage the flag themselves);
//   2. signed license file "2papi.license" (Ed25519, offline) whose payload
//     edition is cloud|ent — unlocks only the features listed in it;
//   3. otherwise OSS: nothing enterprise-gated responds.

export type Edition = 'oss' | 'cloud' | 'ent';

const EDITION_ENV = '2PAPI_EDITION';
const PUBKEY_ENV = '2PAPI_LICENSE_PUBKEY';
const LICENSE_FILE = '2papi.license';

// Must match knownFeatures in internal/license/license.go — unknown flags
// fail validation so typos can't silently unlock nothing.
const KNOWN_FEATURES = new Set([
  'sso', 'orgs', 'audit_export', 'secrets', 'ipacl', 'guardrails',
  'multiregion', 'branding', 'cc_gateway',
]);

interface LicensePayload {
  ed: string;
  cid?: string;
  cap?: number;
  iat?: number;
  exp?: number;
  f?: string[];
  nbf?: number;
  trial?: boolean;
}

interface Resolved {
  edition: Edition;
  features: Set<string>;
}

let cache: { mtimeMs: number; resolved: Resolved } | null = null;

function publicKeyFromHex(hexKey: string): crypto.KeyObject | null {
  const raw = Buffer.from(hexKey.trim(), 'hex');
  if (raw.length !== 32) return null;
  // Raw Ed25519 public key wrapped in SPKI DER for node:crypto.
  const spki = Buffer.concat([Buffer.from('302a300506032b6570032100', 'hex'), raw]);
  try {
    return crypto.createPublicKey({ key: spki, format: 'der', type: 'spki' });
  } catch {
    return null;
  }
}

// validateLicense mirrors license.Validate in Go: format
// <prefix>:<base64url(payload)>.<base64url(sig)> where sig covers
// "<prefix>.<b64payload>" so swapping the edition prefix breaks it.
export function validateLicense(text: string, pubkeyHex: string, now = new Date()): { edition: Edition; features: Set<string> } {
  const s = text.trim();
  const colon = s.indexOf(':');
  const dot = s.lastIndexOf('.');
  if (colon <= 0 || dot <= colon + 1 || dot === s.length - 1) throw new Error('bad license format');
  const prefix = s.slice(0, colon);
  if (prefix !== 'ent' && prefix !== 'cloud') throw new Error('bad license prefix');

  const pub = publicKeyFromHex(pubkeyHex);
  if (!pub) throw new Error('no trusted public key configured');

  let payload: LicensePayload;
  try {
    const b64payload = s.slice(colon + 1, dot);
    const b64sig = s.slice(dot + 1);
    const msg = Buffer.from(`${prefix}.${b64payload}`, 'utf8');
    const sig = Buffer.from(b64sig, 'base64url');
    if (!crypto.verify(null, msg, pub, sig)) throw new Error('bad signature');
    payload = JSON.parse(Buffer.from(b64payload, 'base64url').toString('utf8'));
  } catch {
    throw new Error('bad license signature or payload');
  }
  if (payload.ed !== prefix) throw new Error('edition mismatch');
  const features = payload.f ?? [];
  for (const f of features) {
    if (!KNOWN_FEATURES.has(f)) throw new Error(`unknown feature ${f}`);
  }
  const unix = Math.floor(now.getTime() / 1000);
  if (payload.exp && payload.exp > 0 && unix >= payload.exp) throw new Error('license expired');
  if (payload.nbf && payload.nbf > 0 && unix < payload.nbf) throw new Error('license not valid yet');
  return { edition: prefix as Edition, features: new Set(features) };
}

function resolve(): Resolved {
  const envEdition = (process.env[EDITION_ENV] ?? '').toLowerCase().trim();
  if (envEdition) {
    if (envEdition === 'cloud' || envEdition === 'ent') {
      return { edition: envEdition, features: new Set(KNOWN_FEATURES) };
    }
    return { edition: 'oss', features: new Set() };
  }
  let stat: fs.Stats;
  try {
    stat = fs.statSync(LICENSE_FILE);
  } catch {
    return { edition: 'oss', features: new Set() };
  }
  if (!cache || cache.mtimeMs !== stat.mtimeMs) {
    // Invalid/garbage/expired/wrong-key licenses degrade to OSS.
    let resolved: Resolved = { edition: 'oss', features: new Set() };
    try {
      const text = fs.readFileSync(LICENSE_FILE, 'utf8');
      const validated = validateLicense(text, process.env[PUBKEY_ENV] ?? '');
      resolved = validated;
    } catch {
      resolved = { edition: 'oss', features: new Set() };
    }
    cache = { mtimeMs: stat.mtimeMs, resolved };
  }
  return cache.resolved;
}

export function activeEdition(): Edition {
  return resolve().edition;
}

// hasFeature reports whether an enterprise feature flag is unlocked.
// OSS always answers false — gated routes sleep by default.
export function hasFeature(feature: string): boolean {
  return resolve().features.has(feature);
}

export function requireFeature(feature: string): void {
  if (!hasFeature(feature)) {
    throw new ApiError(
      403,
      'feature_not_licensed',
      `Feature "${feature}" requires an enterprise license (see https://github.com/Rethinger/2papi)`,
    );
  }
}

// requireHosted gates self-serve account flows (signup/login/credits) to
// hosted editions. Plain OSS is a self-hosted proxy: no accounts, no
// telemetry, no signup surface at all.
export function requireHosted(): void {
  const edition = activeEdition();
  if (edition === 'oss') {
    throw new ApiError(
      403,
      'hosted_only',
      'Self-serve accounts are available on 2papi Cloud (see https://github.com/Rethinger/2papi)',
    );
  }
}

// requireOperator gates mutating control APIs on HOSTED editions: only
// users with platform_role='operator' may change state. Plain OSS stays
// open (local tool, loopback) — the enthusiast DX is untouched.
export async function requireOperator(req: Request, db: import('./db').Queryable): Promise<{ id: string; email: string }> {
  const edition = activeEdition();
  if (edition === 'oss') return { id: 'local', email: 'local@oss' };
  const token = req.headers.get('cookie')?.match(/(?:^|;\s*)papi_session=([^;]+)/)?.[1];
  const { resolveSession } = await import('./auth');
  const user = await resolveSession(db, token);
  if (!user) throw new ApiError(401, 'operator_session_required', 'Sign in as an operator to change configuration');
  if (user.platform_role !== 'operator') throw new ApiError(403, 'operator_required', 'Only operators can change configuration');
  return user;
}

// Test hook: reset the mtime cache between tests that rewrite the license file.
export function resetEditionCacheForTests(): void {
  cache = null;
}
