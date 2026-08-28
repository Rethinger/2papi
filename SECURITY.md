# Security Policy

## Supported versions

One binary, three editions (`oss | cloud | ent` — see `docs/strategy-v3.md`).
Security fixes land on `master` and ship in the next patch release; we do not
backport to older tags.

## Reporting a vulnerability

**Do not open a public issue for security reports.**

Email **security@2papi.dev** (or open a GitHub private security advisory via
*Security → Report a vulnerability*). You will get an acknowledgement within
72 hours and a fix timeline within 7 days. Please include:

- affected version / commit;
- minimal reproduction (config + request is usually enough);
- impact assessment.

We credit reporters in the release notes unless you prefer otherwise.

## Scope notes

- The gateway never forwards client `Authorization` upstream; upstream
  credentials are replaced server-side and encrypted at rest in the
  control-plane (`secret_records`, envelope encryption).
- Enterprise features (SSO/OIDC, organizations, audit export) are gated by an
  offline Ed25519 license and degrade to OSS without one — missing licenses
  must fail closed (see `internal/edition`, `control-plane/lib/edition.ts`).
- Self-hosted OSS ships with zero telemetry: nothing leaves the box except
  calls to the upstream providers you configure.

## Hardening checklist for deployments

- Change every dev default secret (`CONTROL_PLANE_MASTER_KEY`,
  `INTERNAL_SERVICE_TOKEN`, `GATEWAY_SHARED_SECRET`) — production runtime
  refuses known development values.
- Put TLS in front of both gateway and dashboard (compose ships Caddy-ready).
- Restrict dashboard exposure; operator APIs are not authenticated in OSS —
  bind them to localhost or your internal network.
