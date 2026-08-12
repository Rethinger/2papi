# Papi Control Plane

Phase A dashboard and control-plane foundation. The root `compose.yaml` integrates it with PostgreSQL, Redis, and the Go gateway.

The dashboard includes local-only OpenAI Codex OAuth/Device Code/auth-file onboarding, model discovery, and durable quota-reset operations. See [../docs/codex-provider.md](../docs/codex-provider.md) for operator requirements and release validation commands.

## Required services

- PostgreSQL 17, private service, `DATABASE_URL=postgres://postgres:postgres@postgres:5432/papi_control`
- Redis 7, private service, `REDIS_URL=redis://redis:6379`

## Required environment

- `CONTROL_PLANE_MASTER_KEY`: base64 or hex encoded 32-byte AES-GCM master key.
- `INTERNAL_SERVICE_TOKEN`: bearer token used by gateways for `/api/internal/snapshots`.
- `GATEWAY_SHARED_SECRET`: HMAC secret for virtual API key hashes.
- `CONTROL_PLANE_BIND_HOST=127.0.0.1` for local-only dashboard binding.
- `DASHBOARD_PUBLIC_URL=http://localhost:13000` for safe Codex auth redirects.
- `TRUSTED_PROXY=false` by default. Leave disabled unless a trusted proxy owns `X-Forwarded-Host`.

## Codex authentication

The Codex provider supports three local login flows:

- Browser OAuth starts at `POST /api/control/v1/codex/oauth/start` and returns an authorization URL. The OpenAI callback is `GET /auth/callback` on the loopback listener `localhost:1455`.
- Device login starts at `POST /api/control/v1/codex/device/start`; poll `GET /api/control/v1/codex/device/{session}/status` until `pending`, `slow_down`, `denied`, `expired`, `failed`, or `complete`.
- Existing `auth.json` import posts the file body to `POST /api/control/v1/codex/import-auth`. Bodies above 1 MiB are rejected with `413`.

Successful logins create or rotate an encrypted Codex account credential, persist verified profile fields, store a new draft config, and audit only account ID, method, plan, and revision. Callback redirects always go to `DASHBOARD_PUBLIC_URL` with only `codex_status` and, on success, `account_id`.

For Docker Compose, the control plane binds both `127.0.0.1:13000:3000` for the dashboard and `127.0.0.1:1455:3000` for the OAuth loopback callback.

## Local commands

```sh
npm install
npm run migrate
npm run seed
npm run dev
npm test
```

Management APIs live under `/api/control/v1`. The dashboard is served at `/`. Gateway snapshot fetch and acknowledgement use authenticated `/api/internal/v1/snapshot` and `/api/internal/v1/gateway-acks` endpoints.
