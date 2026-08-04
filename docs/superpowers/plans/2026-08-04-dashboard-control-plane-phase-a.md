# Implementation Plan: Dashboard Control Plane Phase A

Date: 2026-08-04
Design: `docs/superpowers/specs/2026-08-04-dashboard-control-plane-design.md`

## Outcome

Deliver a Docker-first local control plane with a functional card-first management UI, PostgreSQL persistence, Redis coordination, encrypted credentials, management CRUD, versioned snapshot publication, and live snapshot adoption by the Go gateway without request-path database access.

## Stack

- Next.js 16, React 19, TypeScript.
- PostgreSQL 17 with SQL migrations and the `pg` driver.
- Redis 7 with `ioredis`.
- Zod for API and snapshot validation.
- Node `crypto` AES-256-GCM envelope encryption.
- Existing Go gateway extended with a snapshot source and atomic runtime swap.
- Docker Compose as the primary development and E2E environment.

## Task 1: Control-plane foundation

1. Create `control-plane/` as a standalone Next.js application.
2. Add environment validation, structured errors, health/readiness routes, database pool, Redis client, and migration runner.
3. Add a production multi-stage Dockerfile.
4. Extend Compose with PostgreSQL, Redis, and control-plane services. Do not publish database ports.
5. Bind dashboard to `127.0.0.1:3000` by default.

## Task 2: Durable schema

Add migrations for:

- providers;
- encrypted secret records;
- accounts;
- model aliases and account mappings;
- routing settings;
- virtual keys;
- system settings;
- audit events;
- configuration versions;
- gateway acknowledgements.

Seed the current generic OpenAI provider, two fake accounts, `gpt-dev`, the development virtual key, and balanced routing when the database is empty.

## Task 3: Secret management

1. Parse a 32-byte master key from the environment.
2. Generate a random data key per credential.
3. Encrypt credential JSON with AES-256-GCM.
4. Encrypt the data key with the master key using AES-256-GCM.
5. Store nonces, ciphertext, authentication tags, and key version.
6. Return only secret presence and rotation metadata from read APIs.
7. Test round trips, wrong keys, tampering, and redaction.

## Task 4: Management API

Implement `/api/control/v1` resources:

- overview summary;
- providers;
- accounts;
- models and account assignments;
- routing settings;
- virtual keys;
- settings;
- audit events;
- configuration versions and publication.

Mutations run inside PostgreSQL transactions, use Zod validation, record audit events, and trigger compilation of a new draft snapshot. Destructive actions remain explicit and preserve historical references where required.

## Task 5: Snapshot compiler and distribution

1. Read one consistent PostgreSQL transaction.
2. Decrypt only credentials required by enabled accounts.
3. Compile the existing Go-compatible version-1 configuration shape plus metadata fields for version and checksum.
4. Validate references and ensure every model has an eligible account.
5. Store the immutable snapshot in `config_versions`.
6. Publish version and checksum through Redis.
7. Expose an authenticated internal fetch endpoint.
8. Accept gateway acknowledgement and error reports.
9. Support rollback by cloning a previous valid snapshot into a new version.

## Task 6: Go runtime snapshot adoption

1. Replace fixed server snapshot ownership with an atomic runtime object.
2. Rebuild auth, router, proxy, and derived maps before swapping the runtime pointer.
3. Add a control-plane snapshot client using an internal service token.
4. Poll at startup and periodically; Redis remains an optimization, not a hard dependency.
5. Validate version, checksum, references, and credentials.
6. Keep the last valid runtime on failure.
7. Acknowledge successful and failed adoption.
8. Preserve YAML startup as a fallback when the control plane is unavailable.

## Task 7: Phase A management UI

Implement a dark card-first shell with:

- compact sidebar;
- Overview health/configuration cards;
- Accounts page with create, enable/disable, routing metadata, credential replacement, and state badges;
- Models & Routes page with aliases, upstream ids, account assignments, strategy, and max attempts;
- API Keys page with one-time plaintext display on creation;
- Settings page;
- Audit page;
- configuration version status and Publish/Rollback controls.

Phase A uses server components for reads and client components only for interactive forms. Full drag-and-drop layouts and analytics widgets remain Phase B.

## Task 8: Verification

1. Node unit tests for encryption, validation, snapshot compilation, and redaction.
2. PostgreSQL integration tests for migrations, constraints, CRUD transactions, audits, and rollback.
3. Go race tests for atomic runtime swaps during concurrent requests.
4. Docker Compose E2E:
   - initialize and seed database;
   - open dashboard;
   - create an account and model route;
   - publish configuration;
   - observe gateway acknowledgement;
   - call streaming completion through the new route;
   - modify routing and verify adoption without restart;
   - rollback and verify the previous behavior returns.
5. Browser smoke for navigation, account form, publish status, and API-key one-time display.

## Acceptance criteria

- Dashboard loads at `http://localhost:3000` and shows live control-plane and gateway state.
- CRUD changes persist across container restarts.
- Stored upstream credentials cannot be read through the API or database without the master key.
- Publishing creates a versioned snapshot and the Go gateway adopts it without restart.
- PostgreSQL and Redis remain outside request routing hot path.
- An invalid snapshot does not replace the last valid gateway runtime.
- Existing OpenAI-compatible streaming, fallback, rate limiting, and sticky behavior continue passing race tests.
- Docker Compose E2E proves one complete create → publish → adopt → request → rollback workflow.
