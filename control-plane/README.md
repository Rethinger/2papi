# Papi Control Plane

Phase A backend/control-plane foundation. It is intentionally standalone and does not modify the Go gateway or `compose.yaml`.

## Required services

- PostgreSQL 17, private service, `DATABASE_URL=postgres://postgres:postgres@postgres:5432/papi_control`
- Redis 7, private service, `REDIS_URL=redis://redis:6379`

## Required environment

- `CONTROL_PLANE_MASTER_KEY`: base64 or hex encoded 32-byte AES-GCM master key.
- `INTERNAL_SERVICE_TOKEN`: bearer token used by gateways for `/api/internal/snapshots`.
- `GATEWAY_SHARED_SECRET`: HMAC secret for virtual API key hashes.
- `CONTROL_PLANE_BIND_HOST=127.0.0.1` for local-only dashboard binding.

## Local commands

```sh
npm install
npm run migrate
npm run seed
npm run dev
npm test
```

Management APIs live under `/api/control/v1`. Snapshot fetch and gateway acknowledgement use authenticated `/api/internal/snapshots`.
