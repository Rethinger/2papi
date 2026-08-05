# OpenAI Codex Provider Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add secure ChatGPT Codex accounts, model discovery with user-defined public aliases, Codex Responses and Chat Completions proxying, quota tracking, and idempotent reset-credit consumption to 2papi.

**Architecture:** The control plane stores credential-free versioned configuration and materializes encrypted credentials only for authenticated gateway snapshot requests. The Go gateway routes through a deep provider-adapter seam with `openai-compatible` and `openai-codex` adapters, while a private internal server executes discovery, quota, and credential operations outside the public API. Browser OAuth, Device Code, and `auth.json` import converge on one normalized encrypted credential bundle.

**Tech Stack:** Go 1.22 standard library, Next.js and React on Node 22, TypeScript, PostgreSQL 17, Redis 7, Docker Compose, Node test runner, Go test/race/vet, Playwright.

## Global Constraints

- Keep PostgreSQL and Redis outside the gateway request hot path.
- Keep PostgreSQL, Redis, and the gateway internal operation port unexposed to the host.
- Bind dashboard ports as `127.0.0.1:13000:3000` and OAuth callback as `127.0.0.1:1455:3000`.
- Keep the public gateway at `127.0.0.1:18080:8080` and the internal gateway server on container-only port `8081`.
- Preserve schema version 1 YAML startup fallback and last-adopted snapshot behavior during control-plane outages.
- Store no API key, access token, refresh token, ID token, authorization code, or PKCE verifier in `config_versions`, browser storage, audit payloads, quota tables, discovery metadata, or logs.
- Use the fixed production Codex origins `https://auth.openai.com` and `https://chatgpt.com`; test overrides must require `CODEX_TEST_MODE=true`.
- Use `http://localhost:1455/auth/callback` as the production OAuth redirect URI.
- Enforce body limits: auth import 1 MiB, provider operations 2 MiB, credential updates 256 KiB, runtime snapshots 16 MiB.
- Require draft import and explicit publish for discovered models.
- Preserve exact case-sensitive public aliases while rejecting case-folded collisions.
- Never retry a reset-credit consume operation automatically after dispatch may have reached upstream.
- Maintain complete RU and EN dictionary parity.
- Do not add browser fingerprint impersonation or anti-bot bypass behavior.
- Every task follows TDD and ends with a focused commit after its listed validation is green.

---

## File Structure

### Shared snapshot and configuration

- Create `test/fixtures/runtime-snapshot-v2.json` as the cross-language canonical snapshot fixture.
- Create `test/fixtures/runtime-snapshot-v2.sha256` with the expected canonical payload hash.
- Create `control-plane/lib/canonical-json.ts` for deterministic serialization and checksums.
- Create `control-plane/lib/snapshots.ts` for credential-free compilation, runtime materialization, envelope selection, and capability gating.
- Modify `control-plane/lib/control.ts` to keep management schemas and delegate snapshot work.
- Modify `internal/config/config.go` for schema versions 1 and 2 and structured credentials.
- Modify `internal/controlplane/client.go` for dual-format envelopes, capability headers, heartbeats, and extended acknowledgements.

### Control-plane persistence and internal interfaces

- Create `control-plane/migrations/002_snapshot_security.sql` for credential-free history, gateway capabilities, and acknowledgement fields.
- Create `control-plane/migrations/003_codex_provider.sql` for Codex account state, discovered models, and provider operations.
- Create `control-plane/app/api/internal/v1/gateway-heartbeats/route.ts`.
- Create `control-plane/app/api/internal/v1/accounts/[id]/credentials/route.ts`.
- Modify `control-plane/app/api/internal/v1/snapshot/route.ts` and `gateway-acks/route.ts`.
- Modify `control-plane/lib/api.ts` for constant-time internal authentication and bounded JSON reads.

### Gateway adapter and operations modules

- Create `internal/adapter/adapter.go` for the two-method adapter interface and registry.
- Replace `internal/adapter/openai/openai.go` with the OpenAI-compatible adapter.
- Create `internal/adapter/codex/adapter.go`, `auth.go`, `models.go`, `responses.go`, `chat.go`, and `quota.go`.
- Create `internal/operations/server.go` for the private provider-operation server.
- Modify `internal/proxy/proxy.go`, `internal/server/server.go`, and `cmd/gateway/main.go` to use adapters and start both public and internal servers.

### Codex control-plane domain

- Create `control-plane/lib/codex/constants.ts`, `oauth.ts`, `jwt.ts`, `auth-file.ts`, `device.ts`, `accounts.ts`, `operations.ts`, and `quota.ts`.
- Create dedicated Next routes under `control-plane/app/api/control/v1/codex`, `model-discovery`, `models/import-selection`, and `accounts/[id]/quota`.
- Create `control-plane/app/auth/callback/route.ts`.

### Dashboard UI

- Create `control-plane/app/components/codex-account-modal.tsx`.
- Create `control-plane/app/components/codex-account-card.tsx`.
- Create `control-plane/app/components/model-discovery-modal.tsx`.
- Create `control-plane/app/components/codex-quota-panel.tsx`.
- Create `control-plane/app/codex-client.ts` for typed UI requests.
- Modify `control-plane/app/dashboard-client.tsx`, `i18n.ts`, and `styles.css` only for composition, navigation, dictionary entries, and styling.

### Validation

- Add focused TypeScript tests under `control-plane/tests`.
- Add focused Go tests next to each new module.
- Extend `test/fakeupstream/main.go` with a deterministic Codex server.
- Create `test/e2e-codex.mjs`.
- Create `control-plane/tests/codex-dashboard.spec.ts` for Playwright.

---

### Task 1: Canonical Snapshot Fixture and Config Schema Version 2

**Files:**
- Create: `test/fixtures/runtime-snapshot-v2.json`
- Create: `test/fixtures/runtime-snapshot-v2.sha256`
- Create: `control-plane/lib/canonical-json.ts`
- Create: `control-plane/tests/canonical-json.test.ts`
- Modify: `internal/config/config.go`
- Create: `internal/config/config_v2_test.go`
- Create: `internal/controlplane/runtime_checksum_test.go`

**Interfaces:**
- Produces: `canonicalJson(value: unknown): string`
- Produces: `sha256Canonical(value: unknown): string`
- Produces: `config.Credential`, `config.Account.Adapter`, `config.Account.ID`, and version 1 to version 2 normalization.
- Consumed later by: snapshot materialization, gateway adoption, provider adapters, credential refresh.

- [ ] **Step 1: Write the cross-language fixture and failing TypeScript checksum test**

Use one compact JSON fixture whose keys are already canonical. The test must compare both the exact canonical bytes and the committed SHA-256 file:

```ts
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
```

- [ ] **Step 2: Run the TypeScript test and verify it fails**

Run:

```bash
cd control-plane
node --import tsx --test tests/canonical-json.test.ts
```

Expected: FAIL because `lib/canonical-json.ts` does not exist.

- [ ] **Step 3: Implement deterministic serialization**

Create `canonical-json.ts` with recursive object-key sorting, array-order preservation, JSON string escaping, finite-number validation, and SHA-256:

```ts
import crypto from 'node:crypto';

export function canonicalJson(value: unknown): string {
  if (value === null || typeof value === 'boolean' || typeof value === 'string') return JSON.stringify(value);
  if (typeof value === 'number') {
    if (!Number.isFinite(value)) throw new TypeError('canonical JSON rejects non-finite numbers');
    return JSON.stringify(value);
  }
  if (Array.isArray(value)) return `[${value.map(canonicalJson).join(',')}]`;
  if (typeof value === 'object') {
    const record = value as Record<string, unknown>;
    return `{${Object.keys(record).sort().map(key => `${JSON.stringify(key)}:${canonicalJson(record[key])}`).join(',')}}`;
  }
  throw new TypeError(`canonical JSON rejects ${typeof value}`);
}

export function sha256Canonical(value: unknown): string {
  return crypto.createHash('sha256').update(canonicalJson(value)).digest('hex');
}
```

Generate and commit the fixture hash with:

```bash
cd control-plane
node --import tsx -e "import fs from 'node:fs'; import {sha256Canonical} from './lib/canonical-json.ts'; const p='../test/fixtures/runtime-snapshot-v2.json'; fs.writeFileSync('../test/fixtures/runtime-snapshot-v2.sha256', sha256Canonical(JSON.parse(fs.readFileSync(p,'utf8')))+'\n')"
```

- [ ] **Step 4: Write failing Go config version 2 tests**

Cover version 1 API-key normalization and version 2 structured OAuth credentials:

```go
func TestBuildV2CodexAccount(t *testing.T) {
    cfg := validConfig()
    cfg.Version = 2
    cfg.Accounts[0] = Account{
        ID: "00000000-0000-0000-0000-000000000001",
        Name: "codex-main", Adapter: "openai-codex", BaseURL: "https://chatgpt.com/backend-api/codex",
        Enabled: true, Weight: 1, MaxConcurrency: 3,
        Credential: Credential{Kind: "oauth", AccessToken: "at", RefreshToken: "rt", ChatGPTAccountID: "acct", Revision: 7},
    }
    snap, err := Build(cfg)
    if err != nil { t.Fatal(err) }
    if got := snap.AccountsByName["codex-main"].Credential.Revision; got != 7 { t.Fatalf("revision=%d", got) }
}

func TestBuildV1NormalizesAPIKeyCredential(t *testing.T) {
    cfg := validConfig()
    cfg.Version = 1
    cfg.Accounts[0].APIKey = "legacy-key"
    snap, err := Build(cfg)
    if err != nil { t.Fatal(err) }
    if got := snap.AccountsByName[cfg.Accounts[0].Name].Credential.APIKey; got != "legacy-key" { t.Fatalf("api key=%q", got) }
}
```

- [ ] **Step 5: Run the Go tests and verify they fail**

Run:

```bash
go test ./internal/config -run 'TestBuildV[12]' -v
```

Expected: FAIL because structured credential and adapter fields do not exist.

- [ ] **Step 6: Add version 2 configuration types and validation**

Add these exact fields and keep legacy `APIKey` for version 1 input only:

```go
type Credential struct {
    Kind             string `yaml:"kind,omitempty" json:"kind,omitempty"`
    APIKey           string `yaml:"api_key,omitempty" json:"api_key,omitempty"`
    AccessToken      string `yaml:"access_token,omitempty" json:"access_token,omitempty"`
    RefreshToken     string `yaml:"refresh_token,omitempty" json:"refresh_token,omitempty"`
    IDToken          string `yaml:"id_token,omitempty" json:"id_token,omitempty"`
    ExpiresAt        string `yaml:"expires_at,omitempty" json:"expires_at,omitempty"`
    ClientID         string `yaml:"client_id,omitempty" json:"client_id,omitempty"`
    ChatGPTAccountID string `yaml:"chatgpt_account_id,omitempty" json:"chatgpt_account_id,omitempty"`
    Revision         int64  `yaml:"revision,omitempty" json:"revision,omitempty"`
}

type Account struct {
    ID             string     `yaml:"id,omitempty" json:"id,omitempty"`
    Name           string     `yaml:"name" json:"name"`
    Adapter        string     `yaml:"adapter,omitempty" json:"adapter,omitempty"`
    BaseURL        string     `yaml:"base_url" json:"base_url"`
    APIKey         string     `yaml:"api_key,omitempty" json:"api_key,omitempty"`
    Credential     Credential `yaml:"credential,omitempty" json:"credential,omitempty"`
    Enabled        bool       `yaml:"enabled" json:"enabled"`
    Priority       int        `yaml:"priority" json:"priority"`
    Weight         int        `yaml:"weight" json:"weight"`
    MaxConcurrency int        `yaml:"max_concurrency" json:"max_concurrency"`
    Cost           float64    `yaml:"cost" json:"cost"`
}
```

Validation rules:

- version must be 1 or 2
- version 1 maps `APIKey` into `Credential{Kind:"api_key"}`
- version 2 requires account ID, adapter, credential kind, and positive revision
- `openai-compatible` requires `credential.api_key`
- `openai-codex` requires access token, refresh token when available, and ChatGPT account ID

- [ ] **Step 7: Add the Go raw-message checksum fixture test**

Read `runtime-snapshot-v2.json` as bytes through `filepath.Join("..", "..", "test", "fixtures", "runtime-snapshot-v2.json")`, trim only the final newline, and compare `sha256.Sum256(raw)` to the committed hash at the matching `.sha256` path. Go package tests run with the package directory as their working directory. Do not re-marshal in Go and do not depend on the repository invocation directory.

- [ ] **Step 8: Run focused and package tests**

Run:

```bash
cd control-plane && node --import tsx --test tests/canonical-json.test.ts
cd ..
go test ./internal/config ./internal/controlplane -v
```

Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add test/fixtures control-plane/lib/canonical-json.ts control-plane/tests/canonical-json.test.ts internal/config/config.go internal/config/config_v2_test.go internal/controlplane/runtime_checksum_test.go
git commit -m "feat: define canonical snapshot schema v2"
```

---

### Task 2: Credential-Free Configuration History and Database Migrations

**Files:**
- Create: `control-plane/migrations/002_snapshot_security.sql`
- Create: `control-plane/migrations/003_codex_provider.sql`
- Create: `control-plane/lib/snapshot-migration.ts`
- Create: `control-plane/lib/snapshots.ts`
- Modify: `control-plane/lib/control.ts`
- Modify: `control-plane/scripts/migrate.ts`
- Modify: `control-plane/scripts/seed.ts`
- Modify: `control-plane/app/api/control/v1/[[...resource]]/route.ts`
- Modify: `control-plane/tests/integration.test.ts`
- Create: `control-plane/tests/snapshot-security.integration.test.ts`

**Interfaces:**
- Produces: `compileDeclarativeSnapshot(client): Promise<CompiledDeclarativeSnapshot>`
- Produces: `materializeRuntimeSnapshot(client, declarative): Promise<RuntimeSnapshot>`
- Produces tables: `gateway_instances`, `discovered_models`, `account_provider_state`, `provider_operations`.
- Consumes: `canonicalJson`, `sha256Canonical`, existing secret encryption helpers.

- [ ] **Step 1: Write failing migration and no-plaintext tests**

First replace the hard-coded `001_schema.sql` setup in the ordinary integration suite with the same sorted `.sql` loader used by `scripts/migrate.ts`. The dedicated migration test must apply only `001_schema.sql`, seed reconstructable published and rolled-back legacy versions plus one deliberately malformed legacy version containing an API key named `integration-secret`, then apply `002_snapshot_security.sql` and `003_codex_provider.sql`, run the one-time TypeScript backfill, create a new draft, and assert:

```ts
const stored = await client.query('SELECT snapshot::text body FROM config_versions ORDER BY version DESC LIMIT 1');
assert.ok(!stored.rows[0].body.includes('integration-secret'));
assert.ok(!stored.rows[0].body.includes('api_key'));
const declarative = JSON.parse(stored.rows[0].body);
assert.equal(declarative.accounts[0].credential_revision, 1);
assert.equal(declarative.accounts[0].adapter, 'openai-compatible');
```

Also assert:

- reconstructable rows preserve `status`, `source_version`, `published_at`, and rollback lineage
- only the deliberately unreconstructable row becomes `invalid`
- invalid rows receive an appended error object `{code:'snapshot_reconstruction_failed', migration:'002_snapshot_security'}` without replacing prior errors
- `checksum` and `config_checksum` both contain the canonical credential-free checksum during the legacy bridge
- every historical and newly-created `config_versions.snapshot` is free of `api_key`, access tokens, refresh tokens, and ID tokens
- the new tables and columns exist and `provider_operations` has a partial unique index for active resets

- [ ] **Step 2: Run the integration test and verify it fails**

Run inside the control-plane container test database:

```bash
docker compose run --rm control-plane sh -lc "TEST_DATABASE_URL=postgres://postgres:postgres@postgres:5432/papi_control_test npm test"
```

Expected: FAIL because the existing compiler stores decrypted API keys.

- [ ] **Step 3: Add snapshot-security migration**

`002_snapshot_security.sql` must:

```sql
ALTER TABLE accounts ADD COLUMN IF NOT EXISTS auth_type text NOT NULL DEFAULT 'api_key';
ALTER TABLE accounts ADD COLUMN IF NOT EXISTS credential_revision bigint NOT NULL DEFAULT 1 CHECK (credential_revision > 0);

ALTER TABLE config_versions DROP CONSTRAINT IF EXISTS config_versions_status_check;
ALTER TABLE config_versions ADD CONSTRAINT config_versions_status_check CHECK (status IN ('draft','published','rolled_back','invalid'));
ALTER TABLE config_versions ADD COLUMN IF NOT EXISTS schema_version integer NOT NULL DEFAULT 1;
ALTER TABLE config_versions ADD COLUMN IF NOT EXISTS config_checksum text;

CREATE TABLE IF NOT EXISTS snapshot_migration_state (
  migration text PRIMARY KEY,
  completed_at timestamptz NOT NULL DEFAULT now(),
  result jsonb NOT NULL DEFAULT '{}'
);

CREATE TABLE IF NOT EXISTS gateway_instances (
  gateway_id text PRIMARY KEY,
  supported_schemas integer[] NOT NULL DEFAULT '{1}',
  envelope_version integer NOT NULL DEFAULT 1,
  last_seen_at timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE gateway_config_acks ADD COLUMN IF NOT EXISTS schema_version integer NOT NULL DEFAULT 1;
ALTER TABLE gateway_config_acks ADD COLUMN IF NOT EXISTS config_checksum text;
ALTER TABLE gateway_config_acks ADD COLUMN IF NOT EXISTS credential_digest text;
ALTER TABLE gateway_config_acks ADD COLUMN IF NOT EXISTS runtime_checksum text;
ALTER TABLE gateway_config_acks ADD COLUMN IF NOT EXISTS envelope_version integer NOT NULL DEFAULT 1;
```

This SQL migration changes only schema and constraints. It must not blanket-strip JSON or blanket-mark legacy rows invalid.

- [ ] **Step 4: Add Codex persistence migration**

`003_codex_provider.sql` adds typed account profile fields, discovered models, provider state, and operations. Include:

```sql
ALTER TABLE accounts ADD COLUMN IF NOT EXISTS external_account_id text;
ALTER TABLE accounts ADD COLUMN IF NOT EXISTS account_email text;
ALTER TABLE accounts ADD COLUMN IF NOT EXISTS plan_type text;
ALTER TABLE accounts ADD COLUMN IF NOT EXISTS subscription_expires_at timestamptz;
ALTER TABLE accounts ADD COLUMN IF NOT EXISTS token_expires_at timestamptz;
ALTER TABLE accounts ADD COLUMN IF NOT EXISTS last_credential_refresh_at timestamptz;
ALTER TABLE accounts ADD COLUMN IF NOT EXISTS credential_persistence_status text NOT NULL DEFAULT 'persisted';

CREATE UNIQUE INDEX IF NOT EXISTS model_aliases_alias_folded_uq ON model_aliases (lower(alias));

CREATE TABLE IF NOT EXISTS discovered_models (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  provider_id uuid NOT NULL REFERENCES providers(id),
  account_id uuid NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  upstream_model text NOT NULL,
  display_name text NOT NULL,
  capabilities jsonb NOT NULL DEFAULT '{}',
  visibility text NOT NULL DEFAULT 'unknown',
  supported_in_api boolean NOT NULL DEFAULT false,
  available boolean NOT NULL DEFAULT true,
  raw_metadata jsonb NOT NULL DEFAULT '{}',
  first_seen_at timestamptz NOT NULL DEFAULT now(),
  last_seen_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE(account_id, upstream_model)
);

CREATE TABLE IF NOT EXISTS account_provider_state (
  account_id uuid PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE,
  quota jsonb NOT NULL DEFAULT '{}',
  reset_credits jsonb NOT NULL DEFAULT '{}',
  capability_status text NOT NULL DEFAULT 'unknown',
  fetched_at timestamptz,
  last_operation text,
  last_error_code text,
  last_error_message text,
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS provider_operations (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  account_id uuid NOT NULL REFERENCES accounts(id),
  operation_type text NOT NULL,
  idempotency_key text NOT NULL,
  status text NOT NULL CHECK (status IN ('pending','succeeded','failed','unknown')),
  lease_expires_at timestamptz,
  heartbeat_at timestamptz,
  preflight jsonb NOT NULL DEFAULT '{}',
  upstream_request_id text,
  result_summary jsonb NOT NULL DEFAULT '{}',
  warning_code text,
  resolution_source text,
  resolved_by text,
  resolution_note text,
  resolved_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  started_at timestamptz,
  completed_at timestamptz,
  UNIQUE(account_id, operation_type, idempotency_key)
);

CREATE UNIQUE INDEX IF NOT EXISTS provider_operations_one_active_reset
ON provider_operations(account_id)
WHERE operation_type='quota_reset' AND status IN ('pending','unknown');
```

- [ ] **Step 5: Split declarative compilation from runtime materialization**

Move snapshot logic out of `control.ts`. The declarative account shape must contain only:

```ts
{
  id: account.id,
  name: account.name,
  adapter: provider.adapter,
  base_url: account.base_url,
  credential_revision: Number(account.credential_revision),
  enabled: account.enabled,
  priority: account.priority,
  weight: account.weight,
  max_concurrency: account.max_concurrency,
  cost: Number(account.cost)
}
```

`materializeRuntimeSnapshot` loads only the referenced current secret records and returns structured credentials without persisting the runtime object.

- [ ] **Step 6: Implement an idempotent one-time historical backfill**

Add `sanitizeHistoricalConfigVersions(client)` in `snapshot-migration.ts` and invoke it from `scripts/migrate.ts` after all SQL files are applied, under a PostgreSQL advisory lock. In one transaction it must:

1. Return immediately when `snapshot_migration_state` contains `002_snapshot_security`.
2. Lock every existing `config_versions` row in ascending version order.
3. Reconstruct each declarative snapshot from its stored non-secret routing/model/key structure while resolving account IDs, adapter names, and current credential revisions from `providers` and `accounts` source tables.
4. Preserve the original `status`, `source_version`, `published_at`, `created_at`, version number, and prior `errors` for reconstructable rows.
5. Canonically compute `config_checksum`, write it to both `config_checksum` and legacy `checksum`, and replace `snapshot` with the credential-free declarative object.
6. Mark only rows whose account/model/key structure cannot be mapped safely as `invalid`, append the structured migration error, and leave their lineage fields intact. Invalid rows can be viewed but never published or used as a rollback source.
7. Scan the serialized result for forbidden credential field names and the seeded sentinel before committing.
8. Record counts and the completion marker in `snapshot_migration_state` only after all row updates succeed.

This backfill is the security boundary. Recursive key deletion may be used only as a final assertion helper, never as the reconstruction algorithm.

- [ ] **Step 7: Make draft storage and rollback credential-free, then seed a usable baseline**

`storeDraft` stores `schema_version`, canonical `config_checksum`, the same checksum in legacy `checksum`, and the declarative snapshot. Update rollback in `app/api/control/v1/[[...resource]]/route.ts` so it rejects `invalid` sources, copies only a valid declarative snapshot into a new draft, sets `source_version`, refreshes account credential revisions from current source tables, and publishes through the normal capability gate. Rolling back configuration must never restore historical secret bytes or decrement credential revisions. `seed.ts` creates and publishes a new baseline only when no usable published row exists, linking `source_version` to the most recent invalid legacy version when present.

- [ ] **Step 8: Run integration and unit tests**

Run:

```bash
docker compose run --rm control-plane sh -lc "TEST_DATABASE_URL=postgres://postgres:postgres@postgres:5432/papi_control_test npm test"
```

Expected: all tests PASS with zero skips. Reconstructable publish and rollback lineage remain usable, the malformed row is invalid, and no `config_versions.snapshot` contains the seeded secret or forbidden credential fields.

- [ ] **Step 9: Commit**

```bash
git add control-plane/migrations control-plane/lib/snapshot-migration.ts control-plane/lib/snapshots.ts control-plane/lib/control.ts control-plane/scripts/migrate.ts control-plane/scripts/seed.ts control-plane/app/api/control/v1 control-plane/tests
git commit -m "fix: keep credentials out of config history"
```

---

### Task 3: Dual-Format Snapshot Endpoint, Gateway Capabilities, and Constant-Time Internal Auth

**Files:**
- Modify: `control-plane/lib/api.ts`
- Modify: `control-plane/lib/control.ts`
- Modify: `control-plane/lib/env.ts`
- Modify: `control-plane/app/api/control/v1/[[...resource]]/route.ts`
- Modify: `control-plane/app/api/internal/v1/snapshot/route.ts`
- Modify: `control-plane/app/api/internal/v1/gateway-acks/route.ts`
- Create: `control-plane/app/api/internal/v1/gateway-heartbeats/route.ts`
- Create: `control-plane/tests/internal-auth.test.ts`
- Create: `control-plane/tests/snapshot-envelope.integration.test.ts`

**Interfaces:**
- Produces: `requireInternal(req, token): void` using length-safe constant-time comparison.
- Produces: `readJsonBounded<T>(req, maxBytes): Promise<T>`.
- Produces legacy and version 2 snapshot envelopes.
- Produces persisted gateway capability state used by publish gating.
- Produces `assertSchemaV2Publishable(client, now): Promise<void>`.

- [ ] **Step 1: Write failing internal-auth tests**

Cover valid token, invalid equal-length token, invalid shorter token, invalid longer token, and oversized body. The body helper must reject before `JSON.parse`.

- [ ] **Step 2: Run the tests and verify failure**

```bash
cd control-plane
node --import tsx --test tests/internal-auth.test.ts
```

Expected: FAIL because comparison is ordinary string equality and no bounded reader exists.

- [ ] **Step 3: Implement constant-time auth and bounded reads**

Use fixed-length SHA-256 digests:

```ts
function digest(value: string): Buffer { return crypto.createHash('sha256').update(value).digest(); }
export function requireInternal(req: Request, token: string) {
  const got = req.headers.get('authorization')?.replace(/^Bearer\s+/i, '') ?? '';
  if (!crypto.timingSafeEqual(digest(got), digest(token))) throw new ApiError(401, 'unauthorized', 'Invalid internal service token');
}
```

`readJsonBounded` must stream or read the array buffer, reject when `content-length` or actual bytes exceed the limit, then parse.

- [ ] **Step 4: Write failing dual-envelope integration tests**

Test the snapshot bridge and publication gate:

1. No capability headers returns legacy `{version, checksum, snapshot}` with materialized version 1 configuration.
2. Headers `X-Gateway-Snapshot-Schemas: 1,2` and `X-Gateway-Envelope-Version: 2` return `{config_version,schema_version,config_checksum,credential_digest,runtime_checksum,snapshot}`.
3. Persisted gateway capability version 1 plus a published Codex account returns 426 `upgrade_required`.
4. A schema version 2 draft with one active v1-only gateway is rejected before status changes.
5. A schema version 2 draft is publishable only after every active gateway has a latest adopted acknowledgement with envelope version 2 and schema version 2.
6. A gateway older than `GATEWAY_CAPABILITY_TTL` does not block publication, while zero active gateways still fails the configurable `MIN_ACTIVE_GATEWAYS` requirement, default `1`.
7. Rolling back to a valid schema version 1 source remains allowed and materializes current credentials rather than historical credentials.

Assert the runtime checksum matches the exact canonical snapshot bytes.

- [ ] **Step 5: Implement heartbeat, snapshot selection, and acknowledgements**

Heartbeat request:

```json
{"gateway_id":"gateway-compose","supported_schemas":[1,2],"envelope_version":2}
```

Upsert `gateway_instances`. Snapshot selection must consult persisted capability state, not trust request headers alone. Extend acknowledgement validation and insert all identity fields. Define active gateways as `last_seen_at >= now() - GATEWAY_CAPABILITY_TTL`, where the default TTL is three configured snapshot poll intervals.

Before `publishLatest` changes any row status, call `assertSchemaV2Publishable` for drafts whose schema is version 2 or whose declarative contents require version 2. The function locks the draft, requires at least `MIN_ACTIVE_GATEWAYS`, loads each active gateway's latest acknowledgement, and rejects publication unless all have adopted envelope 2 and schema 2. The same transaction performs the publish status change. Request headers alone can update an observation but cannot satisfy the gate. A schema version 1 rollback bypasses the v2 gate but still rejects invalid source versions.

- [ ] **Step 6: Run tests and build**

```bash
cd control-plane
node --import tsx --test tests/internal-auth.test.ts
npm run build
cd ..
docker compose run --rm control-plane sh -lc "TEST_DATABASE_URL=postgres://postgres:postgres@postgres:5432/papi_control_test node --import tsx --test tests/snapshot-envelope.integration.test.ts"
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add control-plane/lib/api.ts control-plane/lib/control.ts control-plane/lib/env.ts control-plane/app/api/control/v1 control-plane/app/api/internal control-plane/tests/internal-auth.test.ts control-plane/tests/snapshot-envelope.integration.test.ts
git commit -m "feat: serve capability-aware runtime snapshots"
```

---

### Task 4: Gateway Dual-Envelope Adoption and Capability Heartbeats

**Files:**
- Modify: `internal/controlplane/client.go`
- Modify: `internal/controlplane/client_test.go`
- Modify: `cmd/gateway/main.go`
- Modify: `cmd/gateway/main_test.go`

**Interfaces:**
- Produces: `controlplane.SnapshotIdentity` with config version, schema version, config checksum, credential digest, and runtime checksum.
- Produces: `Client.Heartbeat(ctx, schemas, envelopeVersion) error`.
- Changes: `Client.Fetch(ctx) (*config.Snapshot, SnapshotIdentity, error)`.
- Consumes: Task 1 config schema and Task 3 envelopes.

- [ ] **Step 1: Write failing client tests for both envelopes**

Use `httptest.Server` fixtures and assert:

```go
snap, id, err := client.Fetch(context.Background())
if err != nil { t.Fatal(err) }
if id.ConfigVersion != 42 || id.SchemaVersion != 2 { t.Fatalf("identity=%+v", id) }
```

Also test checksum mismatch, 426, heartbeat headers/body, and legacy parsing.

- [ ] **Step 2: Run and verify failure**

```bash
go test ./internal/controlplane ./cmd/gateway -run 'Envelope|Heartbeat|Adopt' -v
```

Expected: FAIL because only the legacy identity exists.

- [ ] **Step 3: Implement identity-aware fetch and acknowledgement**

Define:

```go
type SnapshotIdentity struct {
    ConfigVersion int64
    SchemaVersion int
    ConfigChecksum string
    CredentialDigest string
    RuntimeChecksum string
    EnvelopeVersion int
}
func (i SnapshotIdentity) Equal(other SnapshotIdentity) bool {
    return i.ConfigVersion == other.ConfigVersion &&
        i.SchemaVersion == other.SchemaVersion &&
        i.ConfigChecksum == other.ConfigChecksum &&
        i.CredentialDigest == other.CredentialDigest &&
        i.RuntimeChecksum == other.RuntimeChecksum &&
        i.EnvelopeVersion == other.EnvelopeVersion
}
```

Hash the raw `snapshot` bytes before unmarshalling. Send capability headers and heartbeat using the gateway ID. Add a table-driven equality test that changes each field individually and requires `Equal` to return false, plus an exact-copy case that returns true.

- [ ] **Step 4: Update adoption loop**

Replace version/checksum arguments with `SnapshotIdentity`. Skip adoption and acknowledgement only when `identity.Equal(current)` is true. Send heartbeat on each poll interval without adding duplicate config acknowledgements.

- [ ] **Step 5: Run race tests**

```bash
go test -race ./internal/controlplane ./cmd/gateway
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/controlplane cmd/gateway
git commit -m "feat: adopt versioned runtime snapshot envelopes"
```

### Deployment Gate After Tasks 1-4

The commit order is a development dependency order, not permission to deploy the control-plane changes first. Exercise the production-compatible rollout explicitly:

1. Build the Task 4 gateway image and run it against the unchanged legacy control plane. Verify it continues adopting the old envelope while advertising v2 support without requiring it.
2. Deploy the Task 2-3 control plane and migrations only after that gateway behavior is green. Verify the migrated endpoint still serves materialized schema version 1 snapshots to legacy clients.
3. Wait until every active gateway, using the TTL and minimum-count policy above, has an adopted schema 2 and envelope 2 acknowledgement.
4. Only then allow a Codex account or any other schema version 2-only draft to publish.
5. Keep the schema version 1 YAML startup fallback and a valid schema version 1 rollback target throughout the bridge. Do not enable the Compose Codex seed until the acknowledgement gate is green.

Add this sequence to the Compose E2E counters and assertions in Task 15 so rollout is observed, not merely inferred from unit tests.

---

### Task 5: Deep Provider Adapter Seam and OpenAI-Compatible Refactor

**Files:**
- Create: `internal/adapter/adapter.go`
- Replace: `internal/adapter/openai/openai.go`
- Create: `internal/adapter/adapter_test.go`
- Create: `internal/adapter/openai/openai_test.go`
- Modify: `internal/proxy/proxy.go`
- Modify: `internal/proxy/proxy_test.go`
- Modify: `internal/server/server.go`

**Interfaces:**
- Produces exactly:

```go
type Adapter interface {
    Execute(context.Context, Execution) (*Result, error)
    Operate(context.Context, Operation) (OperationResult, error)
}
```

- Produces `Registry.Register(name string, adapter Adapter) error` and `Registry.Get(name string) (Adapter, bool)`.
- Preserves existing OpenAI-compatible behavior through the new seam.

- [ ] **Step 1: Write registry and parity tests**

Test duplicate registration rejection, unknown adapter, request model rewrite, authorization header replacement, streaming passthrough, retry-after handling, and response model alias rewrite.

- [ ] **Step 2: Run existing proxy/server tests to capture baseline**

```bash
go test ./internal/proxy ./internal/server -v
```

Expected: PASS before refactor.

- [ ] **Step 3: Add the adapter types**

Use these shared types and constants:

```go
type Endpoint string
const (
    EndpointChatCompletions Endpoint = "chat_completions"
    EndpointResponses Endpoint = "responses"
)
type OperationKind string
const (
    OperationDiscoverModels OperationKind = "discover_models"
    OperationValidateCredentials OperationKind = "validate_credentials"
    OperationReadUsage OperationKind = "read_usage"
    OperationListResetCredits OperationKind = "list_reset_credits"
    OperationConsumeResetCredit OperationKind = "consume_reset_credit"
)
type Execution struct {
    Endpoint Endpoint
    Request *http.Request
    Account config.Account
    Model config.Model
    PublicModel string
    Body []byte
}
type Result struct { Status int; Header http.Header; Body io.ReadCloser }
type Operation struct { Kind OperationKind; Account config.Account; Input json.RawMessage; IdempotencyKey string }
type OperationResult struct { Data json.RawMessage }
```

- [ ] **Step 4: Move OpenAI-compatible HTTP behavior behind the adapter**

`openai.Adapter.Execute` builds `/v1/chat/completions`, sets the API key, and returns an uncommitted response. `Operate(discover_models)` calls `/v1/models`; unsupported operations return a typed capability error.

- [ ] **Step 5: Make Proxy choose adapter per account**

`Proxy` receives `*adapter.Registry`. It owns routing and retries, while adapters own provider-specific HTTP and protocol behavior. Response commitment remains in `Proxy` so fallback stops after the first client-visible byte.

- [ ] **Step 6: Run parity and full Go tests**

```bash
go test -race ./internal/adapter/... ./internal/proxy ./internal/server
```

Expected: all prior OpenAI-compatible behavior remains green.

- [ ] **Step 7: Commit**

```bash
git add internal/adapter internal/proxy internal/server
git commit -m "refactor: route upstream traffic through provider adapters"
```

---

### Task 6: Private Gateway Operation Server and Credential CAS Persistence

**Files:**
- Create: `internal/operations/server.go`
- Create: `internal/operations/server_test.go`
- Modify: `internal/controlplane/client.go`
- Create: `control-plane/lib/provider-operations.ts`
- Create: `control-plane/app/api/internal/v1/accounts/[id]/credentials/route.ts`
- Create: `control-plane/tests/credential-cas.integration.test.ts`
- Modify: `cmd/gateway/main.go`
- Modify: `compose.yaml`

**Interfaces:**
- Produces: `operations.Server` on `:8081` with `POST /internal/v1/provider-operations`.
- Produces: `controlplane.Client.UpdateCredentials(ctx, accountID, expectedRevision, credential)`.
- Produces: `dispatchProviderOperation(client, accountID, kind, input, idempotencyKey)` in the control plane.
- Produces: CAS credential update returning new revision and credential digest.
- Consumes: adapter registry and encrypted secret-record helpers.

- [ ] **Step 1: Write failing operation-server security tests**

Cover missing token, wrong token, oversized body, unknown operation, an unpublished account carrying a complete one-shot credential bundle, and successful adapter dispatch. Use a fake adapter that records one `Operation`. Capture gateway and control-plane logs in tests and assert the access token, refresh token, ID token, authorization header, and request body never appear.

- [ ] **Step 2: Write failing PostgreSQL CAS tests**

Assert expected revision 7 updates to 8, creates a new encrypted secret record, leaves plaintext absent from storage, and revision 7 replay returns 409 `credential_revision_conflict`.

- [ ] **Step 3: Implement control-plane credential CAS route**

Inside one transaction:

```sql
SELECT credential_revision FROM accounts WHERE id=$1 FOR UPDATE;
```

Reject mismatch. Insert encrypted secret, update `secret_record_id`, increment revision, update token/profile timestamps, audit only safe fields, and return `{credential_revision, credential_digest}`.

The route requires the internal service token with constant-time comparison, rejects bodies above 256 KiB before JSON parsing, and accepts credentials only from the gateway service. A revision conflict returns 409 and causes the gateway sink to trigger an immediate snapshot refresh rather than overwrite newer state.

- [ ] **Step 4: Implement control-plane one-shot operation dispatch**

`dispatchProviderOperation` loads the requested account and its encrypted secret inside a short transaction, decrypts the current credential only in memory, closes the transaction, then POSTs the complete account runtime shape to `GATEWAY_INTERNAL_URL` with the internal service token. This path must work for unpublished accounts because model discovery and credential validation happen before explicit publish. It must never persist the wire request, cache it in Redis, place it in an error payload, or log it. Construct the JSON body directly for the single fetch call, release references in `finally`, and never retain it in module state.

The exact account wire shape is:

```go
type OperationAccount struct {
    ID string `json:"id"`
    Name string `json:"name"`
    Adapter string `json:"adapter"`
    BaseURL string `json:"base_url"`
    Credential config.Credential `json:"credential"`
    Enabled bool `json:"enabled"`
    Priority int `json:"priority"`
    Weight int `json:"weight"`
    MaxConcurrency int `json:"max_concurrency"`
    Cost float64 `json:"cost"`
}
```

The credential contains the actual API key or OAuth access/refresh/ID tokens plus expiry, account ID, client ID, and current revision. This secret-bearing payload is permitted only on the authenticated container-only request and exists only in memory.

- [ ] **Step 5: Implement operation server**

Use SHA-256 constant-time token comparison, `http.MaxBytesReader` at 2 MiB, JSON decoder with unknown-field rejection, adapter lookup, and operation-specific timeouts. Never log request bodies. The request and response wire shapes are:

```json
{
  "operation": "discover_models",
  "account": {
    "id":"account-uuid",
    "name":"codex-main",
    "adapter":"openai-codex",
    "base_url":"https://chatgpt.com/backend-api/codex",
    "credential": {
      "kind":"oauth",
      "access_token":"one-shot-access-token",
      "refresh_token":"one-shot-refresh-token",
      "id_token":"one-shot-id-token",
      "expires_at":"2026-08-05T22:00:00Z",
      "client_id":"app_EMoamEEZ73f0CkXaXp7hrann",
      "chatgpt_account_id":"acct-123",
      "revision":1
    },
    "enabled":true,
    "priority":0,
    "weight":1,
    "max_concurrency":3,
    "cost":0
  },
  "input": {},
  "idempotency_key": ""
}
```

```json
{"data":{},"warning_code":"","credential_revision":1}
```

Decode directly into `OperationAccount`, convert it to `config.Account`, dispatch once, then drop references to the request object. Adapter token refresh persists through `CredentialSink` to the CAS route and returns the resulting revision. A CAS 409 is returned as a typed `credential_revision_conflict` operation error after requesting snapshot refresh.

- [ ] **Step 6: Start the private server and update Compose**

`cmd/gateway/main.go` starts public and internal servers under one cancellation group. Compose adds `GATEWAY_INTERNAL_ADDR=:8081` and `CONTROL_PLANE_URL`, but no `ports` entry for 8081. Control plane receives `GATEWAY_INTERNAL_URL=http://gateway:8081`.

- [ ] **Step 7: Run tests**

```bash
go test -race ./internal/operations ./internal/controlplane ./cmd/gateway
docker compose run --rm control-plane sh -lc "TEST_DATABASE_URL=postgres://postgres:postgres@postgres:5432/papi_control_test node --import tsx --test tests/credential-cas.integration.test.ts"
```

Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/operations internal/controlplane cmd/gateway control-plane/lib/provider-operations.ts control-plane/app/api/internal/v1/accounts control-plane/tests/credential-cas.integration.test.ts compose.yaml
git commit -m "feat: add private provider operation channel"
```

---

### Task 7: Codex OAuth, JWKS Validation, Device Code, and Auth File Domain

**Files:**
- Create: `control-plane/lib/codex/constants.ts`
- Create: `control-plane/lib/codex/jwt.ts`
- Create: `control-plane/lib/codex/oauth.ts`
- Create: `control-plane/lib/codex/device.ts`
- Create: `control-plane/lib/codex/auth-file.ts`
- Create: `control-plane/lib/codex/accounts.ts`
- Modify: `control-plane/lib/redis.ts`
- Modify: `control-plane/lib/env.ts`
- Create: `control-plane/tests/codex-auth.test.ts`

**Interfaces:**
- Produces: `startOAuthSession`, `consumeOAuthSession`, `exchangeAuthorizationCode`.
- Produces: `verifyOpenAIIDToken(token, nonce): Promise<VerifiedCodexIdentity>`.
- Produces: `parseCodexAuthFile(raw): NormalizedCodexCredential`.
- Produces: `startDeviceFlow` and `pollDeviceFlow`.
- Produces normalized credential bundle accepted by account creation and gateway config.

- [ ] **Step 1: Write failing auth-domain tests**

Generate an RSA key pair in the test, expose a fake JWKS response, sign an ID token, and cover signature, issuer, audience, expiry, nonce, plan, email, and account ID. Add fixtures for current `auth.json`, malformed JSON, API-key-only JSON, expired token with refresh token, and unknown containers.

- [ ] **Step 2: Run and verify failure**

```bash
cd control-plane
node --import tsx --test tests/codex-auth.test.ts
```

Expected: FAIL because Codex auth modules do not exist.

- [ ] **Step 3: Implement pinned constants and test-only origins**

```ts
export const CODEX_CLIENT_ID = 'app_EMoamEEZ73f0CkXaXp7hrann';
export const CODEX_REDIRECT_URI = 'http://localhost:1455/auth/callback';
export const OPENAI_AUTHORIZE_URL = 'https://auth.openai.com/oauth/authorize';
export const OPENAI_TOKEN_URL = 'https://auth.openai.com/oauth/token';
export const OPENAI_JWKS_URL = 'https://auth.openai.com/.well-known/jwks.json';
export const OPENAI_DEVICE_USER_CODE_URL = 'https://auth.openai.com/api/accounts/deviceauth/usercode';
export const OPENAI_DEVICE_TOKEN_URL = 'https://auth.openai.com/api/accounts/deviceauth/token';
export const OPENAI_DEVICE_VERIFICATION_URL = 'https://auth.openai.com/codex/device';
```

Allow overrides only when `env.CODEX_TEST_MODE === true`.

- [ ] **Step 4: Implement Redis one-time sessions**

Add JSON set with TTL and atomic Lua `GET` plus `DEL`. Store state, nonce, verifier, pending account options, and created time. Never return verifier or token values.

- [ ] **Step 5: Implement JWT verification with Node crypto**

Resolve `kid`, build a public key from JWK, verify RS256 signature, then validate issuer, audience, expiry, and nonce. Cache JWKS for at most one hour and refresh once on unknown key ID.

- [ ] **Step 6: Implement PKCE, code exchange, Device Code, and auth-file normalization**

Use `crypto.randomBytes`, S256 challenge, URL-encoded token requests, official Device Code polling states, and strict Zod schemas. When an imported access token or ID token is expired and a refresh token is present, perform exactly one refresh-token exchange with the stored or default client ID, verify the refreshed ID token, and preserve the old refresh token if the response omits a replacement. Reject expired imports without a refresh token. Return:

```ts
type NormalizedCodexCredential = {
  kind: 'oauth'; access_token: string; refresh_token?: string; id_token?: string;
  expires_at: string; client_id: string; chatgpt_account_id: string;
  email?: string; plan_type?: string; auth_method: 'browser'|'device'|'auth_file';
};
```

- [ ] **Step 7: Run tests and build**

```bash
cd control-plane
npm test
npm run build
```

Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add control-plane/lib/codex control-plane/lib/redis.ts control-plane/lib/env.ts control-plane/tests/codex-auth.test.ts
git commit -m "feat: add secure Codex authentication domain"
```

---

### Task 8: Codex Authentication Routes and Localhost Callback Deployment

**Files:**
- Create: `control-plane/app/api/control/v1/codex/oauth/start/route.ts`
- Create: `control-plane/app/auth/callback/route.ts`
- Create: `control-plane/app/api/control/v1/codex/device/start/route.ts`
- Create: `control-plane/app/api/control/v1/codex/device/[session]/status/route.ts`
- Create: `control-plane/app/api/control/v1/codex/import-auth/route.ts`
- Create: `control-plane/app/api/control/v1/accounts/[id]/reauthorize/route.ts`
- Create: `control-plane/tests/codex-auth-routes.test.ts`
- Modify: `compose.yaml`
- Modify: `control-plane/README.md`

**Interfaces:**
- Produces the exact auth routes from the spec.
- Produces account rows and encrypted credentials through one `createCodexAccount` function.
- Consumes Task 7 auth domain and Task 2 draft compilation.

- [ ] **Step 1: Write failing route-handler tests**

Invoke dependency-injected route core functions with synthetic Requests so tests never use the production database. Cover direct `Host: localhost:1455`, spoofed `X-Forwarded-Host`, invalid state, reused state, callback error, successful redirect to `DASHBOARD_PUBLIC_URL`, import size 413, and Device Code state transitions. The actual Next route files must only construct dependencies and call the tested core functions.

- [ ] **Step 2: Implement shared account creation**

`createCodexAccount(client, input)` must insert or rotate the encrypted credential, write verified profile columns, audit only account ID, method, plan, and revision, then compile a draft.

- [ ] **Step 3: Implement routes with fixed redirects and bounded bodies**

Callback host validation uses the direct URL and `Host` header. Ignore forwarded host headers unless `TRUSTED_PROXY=true`, which defaults false. Redirect query parameters contain only safe `codex_status` and account ID.

- [ ] **Step 4: Add loopback callback mapping**

Compose ports become:

```yaml
ports:
  - "127.0.0.1:13000:3000"
  - "127.0.0.1:1455:3000"
```

Add `DASHBOARD_PUBLIC_URL=http://localhost:13000`.

- [ ] **Step 5: Run route tests and Next build**

```bash
cd control-plane
node --import tsx --test tests/codex-auth-routes.test.ts
npm run build
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add control-plane/app/api/control/v1/codex control-plane/app/api/control/v1/accounts control-plane/app/auth control-plane/tests/codex-auth-routes.test.ts control-plane/README.md compose.yaml
git commit -m "feat: add Codex browser device and import login"
```

---

### Task 9: Codex Adapter Authentication, Refresh Singleflight, and Model Discovery

**Files:**
- Create: `internal/adapter/codex/adapter.go`
- Create: `internal/adapter/codex/auth.go`
- Create: `internal/adapter/codex/models.go`
- Create: `internal/adapter/codex/auth_test.go`
- Create: `internal/adapter/codex/models_test.go`
- Modify: `cmd/gateway/main.go`

**Interfaces:**
- Produces: `codex.New(client *http.Client, sink CredentialSink, refresh SnapshotRefreshTrigger, options Options) *Adapter`.
- Produces:

```go
type CredentialPersistResult struct {
    Revision int64
    Digest string
}
type CredentialSink interface {
    Persist(context.Context, string, int64, config.Credential) (CredentialPersistResult, error)
}
type SnapshotRefreshTrigger interface {
    TriggerSnapshotRefresh(reason string)
}
type Options struct {
    TestMode bool
    AuthBaseURL string
    BackendBaseURL string
    ClientVersion string
    Now func() time.Time
}
```

- Produces refresh singleflight keyed by account ID and revision.
- Implements `Operate(discover_models)` and `Operate(validate_credentials)`.
- Consumes config credentials, adapter interface, and control-plane credential sink.

- [ ] **Step 1: Write failing refresh tests**

Launch 20 goroutines against an expired account and assert the fake token endpoint receives one refresh request, all callers get the new access token, and the credential sink receives one CAS update returning both revision and digest. Test refresh response without a new refresh token preserves the old token. Add cases for temporary persistence failure with bounded retry, successful retry updating local revision, and `controlplane.ErrCredentialRevisionConflict` triggering exactly one coalesced snapshot refresh signal while returning typed `codex.ErrSnapshotRefreshRequired`.

- [ ] **Step 2: Write failing model-discovery tests**

Fake `/backend-api/codex/models` must receive bearer token, `ChatGPT-Account-ID`, and `client_version`. Parse only bounded JSON. Preserve slug, visibility, `supported_in_api`, context window, and capabilities. A 401 triggers one refresh; a second 401 fails.

- [ ] **Step 3: Implement token manager**

Use a mutex-protected map of in-flight refresh calls. Refresh five minutes before expiry. Update local state first, then persist through `CredentialSink`. On success, replace the local revision with `CredentialPersistResult.Revision` and retain the digest for diagnostics without exposing it publicly. On transient persistence failure, retain the fresh in-memory credential, expose the typed degraded-persistence warning, and retry with bounded exponential backoff capped at the snapshot poll interval. On revision conflict, do not retry the CAS, invoke `SnapshotRefreshTrigger.TriggerSnapshotRefresh("credential_revision_conflict")`, and return `ErrSnapshotRefreshRequired`.

`controlplane.Client.UpdateCredentials` is the concrete `CredentialSink`: it authenticates the 256 KiB bounded PUT, decodes `{credential_revision,credential_digest}`, maps HTTP 409 to the exported sentinel `controlplane.ErrCredentialRevisionConflict`, and redacts response bodies from errors. `cmd/gateway/main.go` owns a capacity-one refresh channel implementing `SnapshotRefreshTrigger`; both the periodic adoption loop and conflict signals call the same atomic fetch/build/swap path, so bursts coalesce and no second adoption loop is created. Pass these concrete instances into the single registered Codex adapter used by public inference and the private operation server.

- [ ] **Step 4: Implement pinned Codex requests and discovery operation**

Production base URL is fixed. `CODEX_TEST_MODE` permits the fake origin. Reject a configured Codex base URL outside the allowed set.

- [ ] **Step 5: Register Codex adapter and run tests**

```bash
go test -race ./internal/adapter/codex ./internal/adapter/... ./cmd/gateway
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/adapter/codex cmd/gateway
git commit -m "feat: add Codex credential refresh and discovery adapter"
```

---

### Task 10: Persisted Model Discovery, Selection, Aliases, and Rename Safety

**Files:**
- Create: `control-plane/lib/codex/operations.ts`
- Create: `control-plane/app/api/control/v1/model-discovery/route.ts`
- Create: `control-plane/app/api/control/v1/discovered-models/route.ts`
- Create: `control-plane/app/api/control/v1/models/import-selection/route.ts`
- Create: `control-plane/app/api/control/v1/models/[id]/rename/route.ts`
- Create: `control-plane/tests/codex-discovery.integration.test.ts`
- Modify: `control-plane/lib/control.ts`

**Interfaces:**
- Produces discovery scopes `all`, `provider_id`, and `account_id`.
- Produces persisted per-account discovery results with partial success.
- Produces atomic `importSelection` and `renameModelAlias` operations.
- Consumes private gateway operation endpoint.

- [ ] **Step 1: Write failing discovery integration tests**

Seed two accounts, fake one success and one failure, run `all`, and assert successful models persist while the failed account returns a safe error. On the second run, omit one model and assert it becomes unavailable rather than deleted.

- [ ] **Step 2: Write failing alias safety tests**

Assert:

- `luna-code` is accepted exactly.
- `Luna-Code` conflicts with `luna-code`.
- whitespace and control characters fail.
- imported selection creates a draft, not a published version.
- rename updates `virtual_keys.models` transactionally.
- failed dependent update rolls back the alias rename.

- [ ] **Step 3: Implement typed gateway operation client**

Use `GATEWAY_INTERNAL_URL`, internal bearer token, 2 MiB response limit, per-account timeout, and bounded concurrency of four. Return per-account results instead of failing the entire batch.

- [ ] **Step 4: Implement discovery persistence and import selection**

Upsert `(account_id, upstream_model)`, sanitize raw metadata to 32 KiB, mark missing rows unavailable, and audit counts only. Group identical upstream slugs for UI response but retain per-account rows.

- [ ] **Step 5: Implement exact alias validation and rename**

Use `/^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$/` plus no trailing separator. Rename `model_aliases.alias` and each matching element in `virtual_keys.models` inside one transaction before `storeDraft`.

- [ ] **Step 6: Run PostgreSQL integration tests**

```bash
docker compose run --rm control-plane sh -lc "TEST_DATABASE_URL=postgres://postgres:postgres@postgres:5432/papi_control_test npm test"
```

Expected: PASS with zero skips.

- [ ] **Step 7: Commit**

```bash
git add control-plane/lib/codex/operations.ts control-plane/app/api/control/v1/model-discovery control-plane/app/api/control/v1/discovered-models control-plane/app/api/control/v1/models control-plane/tests/codex-discovery.integration.test.ts control-plane/lib/control.ts
git commit -m "feat: add model discovery and public alias import"
```

---

### Task 11: Native Codex Responses Proxy

**Files:**
- Create: `internal/adapter/codex/responses.go`
- Create: `internal/adapter/codex/responses_test.go`
- Modify: `internal/server/server.go`
- Modify: `internal/server/runtime_test.go`
- Modify: `internal/protocol/protocol.go`

**Interfaces:**
- Adds public `POST /v1/responses`.
- Codex adapter implements native Responses execution and SSE passthrough.
- Preserves public alias in non-stream and stream response model fields.

- [ ] **Step 1: Write failing non-stream and SSE tests**

Test request alias rewrite, pinned upstream path `/backend-api/codex/responses`, auth headers, safe header filtering, non-stream response alias rewrite, SSE alias rewrite, `codex.rate_limits` observation, and no fallback after first flushed event.

- [ ] **Step 2: Run and verify failure**

```bash
go test ./internal/adapter/codex ./internal/server -run 'Responses|SSE' -v
```

Expected: FAIL because route and converter do not exist.

- [ ] **Step 3: Add endpoint-aware request metadata**

Define one parser that extracts model, stream, user, and metadata for both public endpoints without decoding provider-specific fields that adapters own.

- [ ] **Step 4: Implement native Responses execution**

Rewrite only the model field, reject unsupported or malformed inputs before upstream dispatch, set Codex headers, stream without full buffering, and transform only JSON/SSE model fields and safe rate-limit observations.

- [ ] **Step 5: Add public route and policy checks**

Reuse virtual-key authentication, model allowlist, RPM, routing, cooldown, and circuit rules. Return the existing structured gateway error shape.

- [ ] **Step 6: Run race tests**

```bash
go test -race ./internal/adapter/codex ./internal/server ./internal/proxy
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/adapter/codex/responses.go internal/adapter/codex/responses_test.go internal/server internal/protocol
git commit -m "feat: proxy native Codex Responses traffic"
```

---

### Task 12: Chat Completions to Responses Compatibility

**Files:**
- Create: `internal/adapter/codex/chat.go`
- Create: `internal/adapter/codex/chat_test.go`
- Modify: `internal/adapter/codex/adapter.go`

**Interfaces:**
- Converts supported `/v1/chat/completions` input to Codex Responses.
- Converts non-stream and SSE Responses output into OpenAI Chat Completions shapes.
- Returns typed `codex_feature_unsupported` errors for unrepresentable fields.

- [ ] **Step 1: Write table-driven request conversion tests**

Cover system, developer, user, assistant, tool messages, text, function tools, tool choice, tool results, token limit, reasoning effort, stream, and stop. Assert image and structured-output input fails when capability is absent.

- [ ] **Step 2: Write response conversion tests**

Cover text output, tool calls, usage, finish reasons, streaming deltas, one role delta, indexed tool-call deltas, final chunk, and `[DONE]`.

- [ ] **Step 3: Implement typed conversion structs**

Do not convert through unrestricted `map[string]any`. Define bounded request and event structs, preserve unknown fields only when explicitly allowlisted, and cap non-stream response buffering at 16 MiB.

- [ ] **Step 4: Connect Chat Completions execution to Codex Responses**

The public endpoint remains unchanged. Adapter selection determines whether the existing OpenAI-compatible request is forwarded directly or converted for Codex.

- [ ] **Step 5: Run focused and full Go tests**

```bash
go test -race ./internal/adapter/codex ./internal/server ./internal/proxy
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/adapter/codex/chat.go internal/adapter/codex/chat_test.go internal/adapter/codex/adapter.go
git commit -m "feat: translate chat completions for Codex"
```

---

### Task 13: Codex Usage, Reset Credits, and Durable Reset State Machine

**Files:**
- Create: `internal/adapter/codex/quota.go`
- Create: `internal/adapter/codex/quota_test.go`
- Create: `control-plane/lib/codex/quota.ts`
- Create: `control-plane/app/api/control/v1/accounts/[id]/quota/route.ts`
- Create: `control-plane/app/api/control/v1/accounts/[id]/quota/refresh/route.ts`
- Create: `control-plane/app/api/control/v1/accounts/[id]/quota/reset/route.ts`
- Create: `control-plane/app/api/control/v1/accounts/[id]/quota/reset/[operation]/reconcile/route.ts`
- Create: `control-plane/app/api/control/v1/accounts/[id]/quota/reset/[operation]/resolve/route.ts`
- Create: `control-plane/tests/codex-quota.integration.test.ts`

**Interfaces:**
- Adapter operations: `read_usage`, `list_reset_credits`, `consume_reset_credit`.
- Control-plane state transitions: pending, succeeded, failed, unknown.
- Enforces one pending or unknown reset per account and one upstream consume per idempotency key.

- [ ] **Step 1: Write failing Go quota parser tests**

Use recorded safe fixtures for primary and secondary windows, additional limits, plan, credit count, credit expiration, malformed contract, 403 unsupported, and successful consume body containing the exact stored `redeem_request_id`.

- [ ] **Step 2: Implement quota adapter operations**

Use normal Go HTTP transport and pinned `wham` URLs. Sanitize errors to stable codes. Return capability unsupported on 403 or contract change without affecting inference health.

- [ ] **Step 3: Write failing PostgreSQL reset-state tests**

Cover:

- first request inserts pending before dispatch
- same idempotency key returns pending or saved success
- different key while pending returns 409
- lease expiry after dispatch becomes unknown
- unknown blocks later reset
- conclusive reconciliation transitions state
- inconclusive reconciliation stays unknown
- manual resolution requires note and audit
- manual resolution never calls upstream
- later spend requires a new key and live preflight

- [ ] **Step 4: Implement transaction and advisory lock helpers**

Acquire `pg_advisory_xact_lock(hashtextextended(account_id::text, 0))`, then enforce the active-operation index. Store preflight and UUID before calling the gateway operation. Run post-dispatch bookkeeping under a server timeout independent of browser cancellation.

- [ ] **Step 5: Implement refresh, reset, reconcile, and resolve routes**

Refresh persists normalized state. Reset requires `Idempotency-Key` header and confirmation boolean. Reconcile only changes state with conclusive evidence. Resolve requires `{resolution:'succeeded'|'failed', note:string}` and writes a dedicated audit event.

- [ ] **Step 6: Run Go and PostgreSQL tests**

```bash
go test -race ./internal/adapter/codex -run 'Quota|Reset' -v
docker compose run --rm control-plane sh -lc "TEST_DATABASE_URL=postgres://postgres:postgres@postgres:5432/papi_control_test npm test"
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/adapter/codex/quota.go internal/adapter/codex/quota_test.go control-plane/lib/codex/quota.ts control-plane/app/api/control/v1/accounts control-plane/tests/codex-quota.integration.test.ts
git commit -m "feat: add Codex quota and safe reset credits"
```

---

### Task 14: RU/EN Dashboard for Codex Accounts, Discovery, Aliases, and Quota

**Files:**
- Create: `control-plane/app/codex-client.ts`
- Create: `control-plane/app/components/codex-account-modal.tsx`
- Create: `control-plane/app/components/codex-account-card.tsx`
- Create: `control-plane/app/components/model-discovery-modal.tsx`
- Create: `control-plane/app/components/codex-quota-panel.tsx`
- Modify: `control-plane/app/dashboard-client.tsx`
- Modify: `control-plane/app/i18n.ts`
- Modify: `control-plane/app/styles.css`
- Modify: `control-plane/tests/i18n.test.ts`
- Create: `control-plane/tests/codex-ui.test.ts`

**Interfaces:**
- Typed client methods for every Codex auth, discovery, alias, quota, and reset route.
- Modal and card props contain no credential values.
- Preserves existing locale persistence and Escape behavior.

- [ ] **Step 1: Write failing dictionary parity and UI helper tests**

Add every RU and EN key before component work. Test plan labels, auth states, discovery states, reset warnings, unknown operation resolution, and alias validation messages. Assert no raw known status is rendered.

- [ ] **Step 2: Create the typed client**

Use one `codexRequest<T>` wrapper around same-origin fetch. Never retry mutations automatically. For reset, generate one UUID in the component, send it as `Idempotency-Key`, and reuse it only while displaying the same operation.

- [ ] **Step 3: Implement add-account modal**

Three tabs: browser, Device Code, auth file. Add optional local name and routing fields. Browser tab opens the returned URL. Device tab copies code and polls local status. Import uses a file input, 1 MiB client precheck, and never displays file content.

- [ ] **Step 4: Implement account and quota panels**

Show localized plan, email/display name, auth method, token expiry, last refresh, quota bars, reset times, reset-credit count and expiry, stale state, unsupported state, Usage link, confirmation dialog, unknown reconciliation, and manual resolution warning.

- [ ] **Step 5: Implement model discovery modal**

Add Fetch menu for all/provider/account, per-account progress, partial failures, grouped models, checkboxes, editable public alias, account mappings, conflict action, unavailable state, and `Import selected` draft creation.

- [ ] **Step 6: Compose components into the dashboard**

Keep `dashboard-client.tsx` responsible for navigation and shared data only. Do not add Codex protocol logic to it. Use Octicons already installed for browser, copy, refresh, download, warning, and model actions.

- [ ] **Step 7: Run unit tests and Next build**

```bash
cd control-plane
npm test
npm run build
```

Expected: PASS with dictionary parity.

- [ ] **Step 8: Commit**

```bash
git add control-plane/app/codex-client.ts control-plane/app/components control-plane/app/dashboard-client.tsx control-plane/app/i18n.ts control-plane/app/styles.css control-plane/tests
git commit -m "feat: add Codex account and model management UI"
```

---

### Task 15: Fake Codex Upstream and Scripted Compose E2E

**Files:**
- Modify: `test/fakeupstream/main.go`
- Create: `test/e2e-codex.mjs`
- Modify: `compose.yaml`
- Modify: `Dockerfile`
- Modify: `control-plane/Dockerfile`

**Interfaces:**
- Fake Codex server on container port `9010`.
- Deterministic counters endpoint for token refresh, model fetch, inference, quota, and reset consume calls.
- Script cleans up in `finally`, restarts any service it deliberately stopped, and restores the exact initially published baseline.

- [ ] **Step 1: Write fake-upstream contract tests or self-check handlers**

Implement deterministic endpoints:

- `/oauth/authorize`
- `/oauth/token`
- `/api/accounts/deviceauth/usercode`
- `/api/accounts/deviceauth/token`
- `/.well-known/jwks.json`
- `/backend-api/codex/models`
- `/backend-api/codex/responses`
- `/backend-api/wham/usage`
- `/backend-api/wham/rate-limit-reset-credits`
- `/backend-api/wham/rate-limit-reset-credits/consume`
- `/__test/counters`

The reset endpoint must deduplicate by `redeem_request_id` and expose total consumes.

- [ ] **Step 2: Add fake Codex service and health check to Compose**

Extend the existing fake-upstream process so it starts the current ports `9001` and `9002` plus Codex port `9010` under one cancellation group. Add a health endpoint per listener. Use `CODEX_TEST_MODE=true` and test-only origins in both control plane and gateway. Keep port `9010` unexposed to the host and point container-only origins at `http://fake-upstream:9010`.

- [ ] **Step 3: Write the E2E script with `try/finally` cleanup**

Before mutation, record the published config version, all created resource IDs, and the initial service health. The script must execute the 17-step flow from specification section 17.3, including:

```js
const selected = await api('models/import-selection', {
  method: 'POST',
  body: JSON.stringify({ models: [{ discovery_id: discovered.id, alias: 'luna-code', account_ids: [account.id] }] }),
});
assert.equal(selected.published, false);
```

It must verify `/v1/models`, native Responses, Chat Completions, SSE, exact alias rewrite, quota plan, one reset consume across retry, unknown reset blocking, reconciliation, rollback, and continued gateway operation after control-plane stop. It must also exercise the Deployment Gate after Tasks 1-4 by recording a v1-only active gateway, observing schema v2 publish rejection, expiring that observation under a test TTL, recording a v2 acknowledgement, and then observing successful publication.

The `finally` block must be unconditional and ordered:

1. If the test stopped `control-plane`, run `docker compose start control-plane` and wait for its health endpoint before making cleanup API calls.
2. Resolve any test-created `pending` or `unknown` provider operation through the audited test path so it cannot block cleanup.
3. Delete or disable every account, model mapping, key, and test gateway capability row created by the script.
4. Create a rollback draft from the initially published valid version and publish it, or restore the equivalent baseline when the initial version no longer exists.
5. Assert no test alias, account, operation, or capability row remains and all Compose services are healthy.

Any cleanup error is aggregated with the original assertion error rather than replacing it.

- [ ] **Step 4: Build and run Compose E2E**

```bash
docker compose up -d --build
node test/e2e-codex.mjs
```

Expected: PASS with cleanup complete and fake counter `reset_consume=1` for the idempotency retry case.

- [ ] **Step 5: Run existing E2E regression**

```bash
node test/e2e.mjs
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add test/fakeupstream/main.go test/e2e-codex.mjs compose.yaml Dockerfile control-plane/Dockerfile
git commit -m "test: cover Codex compose workflow"
```

---

### Task 16: Playwright Visual QA, Full Validation, and Operator Documentation

**Files:**
- Modify: `control-plane/package.json`
- Modify: `control-plane/package-lock.json`
- Create: `control-plane/playwright.config.ts`
- Create: `control-plane/tests/codex-dashboard.spec.ts`
- Modify: `README.md`
- Modify: `control-plane/README.md`
- Create: `docs/codex-provider.md`

**Interfaces:**
- Adds repeatable desktop and mobile visual validation.
- Documents OAuth port, Device Code fallback, auth import, model fetch, aliases, quota reset risk, test mode, and key-rotation guidance.

- [ ] **Step 1: Add Playwright and failing dashboard tests**

Install `@playwright/test` as a dev dependency and Chromium with `npx playwright install chromium`. Configure:

```ts
import { defineConfig } from '@playwright/test';

export default defineConfig({
  testDir: './tests',
  outputDir: '../artifacts/playwright/codex/results',
  use: {
    baseURL: process.env.PLAYWRIGHT_BASE_URL ?? 'http://127.0.0.1:13000',
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
  },
  projects: [
    { name: 'desktop', use: { viewport: { width: 1440, height: 1000 } } },
    { name: 'mobile', use: { viewport: { width: 390, height: 844 } } },
  ],
});
```

Do not configure a separate Playwright `webServer`. These tests intentionally target the already-built Compose stack so PostgreSQL, Redis, callback port mapping, gateway operations, and fake Codex upstream match release behavior. Test RU and EN. Cover all three auth tabs, callback success/error, Device states, auth-file errors, model fetch, partial discovery, alias collision, draft summary, quota card, reset confirmation, unknown resolution, Escape close, `<html lang>`, console errors, and page overflow. Each project writes explicit stable screenshots to `artifacts/playwright/codex/<project>/<locale>/`.

- [ ] **Step 2: Run Playwright and capture failures**

```bash
docker compose up -d --build
cd control-plane
npx playwright install chromium
npx playwright test tests/codex-dashboard.spec.ts
```

Expected: failures identify any remaining accessibility or layout defects.

- [ ] **Step 3: Fix only verified visual defects**

Use screenshot diffs and browser console output. Add stable test IDs only where semantic role/name selectors are insufficient. Store screenshots under ignored `artifacts/playwright/codex`.

- [ ] **Step 4: Write operator documentation**

Document:

- `127.0.0.1:1455` callback requirement and fallback methods
- never exposing callback or dashboard on non-loopback without explicit security changes
- auth-file location and supported shape
- draft model selection and exact public aliases
- quota endpoint instability and Usage fallback
- reset credit non-refundability and unknown-state resolution
- post-migration recommendation to rotate old API keys because WAL/backups may retain legacy plaintext snapshots
- test-mode origin override restrictions

- [ ] **Step 5: Run the complete validation matrix**

Run:

```bash
cd control-plane
npm run build
cd ..
go test ./...
go test -race ./...
go vet ./...
docker compose up -d --build
docker compose ps
docker compose run --rm control-plane sh -lc "set -e; TEST_DATABASE_URL=postgres://postgres:postgres@postgres:5432/papi_control_test npm test > /tmp/control-tests.tap 2>&1; cat /tmp/control-tests.tap; grep -q '^# skipped 0$' /tmp/control-tests.tap"
cd control-plane
npx playwright install chromium
npx playwright test tests/codex-dashboard.spec.ts
cd ..
node test/e2e.mjs
node test/e2e-codex.mjs
```

Do not use a host `npm test` result as final evidence because database tests intentionally skip without `TEST_DATABASE_URL`. The container command above fails unless the TAP summary reports exactly `# skipped 0`. Expected: every command PASS, every Compose service healthy, no skipped integration tests, no console errors, no horizontal overflow, and RU/EN screenshots for both desktop and mobile produced.

- [ ] **Step 6: Verify security invariants directly**

Query the database and grep logs:

```bash
docker compose exec -T postgres psql -U postgres -d papi_control -Atc "select count(*) from config_versions where snapshot::text ~ '(access_token|refresh_token|id_token|api_key)';"
docker compose logs control-plane gateway | rg -i "access_token|refresh_token|id_token|authorization: bearer"
```

Expected: database count `0`; log grep produces no credential-bearing lines.

- [ ] **Step 7: Commit**

```bash
git add control-plane/package.json control-plane/package-lock.json control-plane/playwright.config.ts control-plane/tests/codex-dashboard.spec.ts README.md control-plane/README.md docs/codex-provider.md
git commit -m "docs: validate and document Codex provider"
```

---

## Spec Coverage Map

- Spec sections 1-3: global constraints and Tasks 1-16.
- Spec sections 4 and 10: Tasks 5, 6, 9, 11, and 12.
- Spec section 5: Tasks 7, 8, 14, and 15.
- Spec section 6: Tasks 1-4, 6, 9, 15, and 16.
- Spec section 7: Tasks 2, 6, 10, and 13.
- Spec sections 8-9: Tasks 8, 10, 13, and 14.
- Spec section 11: Tasks 9, 13, 14, and 15.
- Spec section 12: Task 14 and Playwright coverage in Task 16.
- Spec sections 13-15: Tasks 3, 6-10, 13, and 16.
- Spec section 16 rollout: Tasks 1-6 followed by Tasks 7-16 in the listed order.
- Spec sections 17-18: Tasks 15-16 and the completion gate.
- Spec sections 19-20: global constraints, Task 16 operator documentation, and test-mode restrictions.

## Plan Completion Gate

Before declaring implementation complete, verify every acceptance criterion from `docs/superpowers/specs/2026-08-05-openai-codex-provider-design.md` maps to a green automated or visual check above. Record final evidence in the todo goal assessment, including exact command results, Compose health, screenshot paths, E2E reset consume count, database secret scan, and git commit range.
