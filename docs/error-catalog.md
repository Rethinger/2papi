# Error catalog

Canonical machine-readable errors. Control-plane returns
`{"error":{"code","message","details?"}}`; the gateway returns OpenAI-style
`{"error":{"code","message","type":"gateway_error"}}` or a bare message for
legacy paths.

## Gateway (/v1/*)

| HTTP | code / reason | Meaning |
|---|---|---|
| 400 | `invalid json` / `invalid payload` / `invalid body` / `invalid multipart form` | Request body failed parsing/size limits |
| 400 | `model required` | No model in request |
| 401 | `unauthorized` | Missing/unknown virtual key |
| 403 | `model not allowed` | Key allowlist excludes the alias |
| 404 | `unknown model` / `unknown mcp server` | Alias or MCP server not in snapshot |
| 405 | `method not allowed` | Wrong verb |
| 429 | `rate_limited` | RPM/TPM token bucket empty |
| 429 | `budget_exceeded` | Key/team/org/balance budget exhausted |
| 429 | `concurrency_limited` | Max in-flight requests reached |
| 502 | `all upstream attempts failed` / `adapter unavailable` / `count_tokens failed` | Every attempt failed |
| 503 | `no healthy upstream account` | All accounts cooling/locked/saturated |
| 501 | `count_tokens unsupported by this account` | Adapter lacks the endpoint |

Diagnostic headers on success/failure: `X-Gateway-Route`, `-Attempts`,
`-Overhead-MS` (pure gateway cost), `-Upstream-MS`, `-Proxy`,
`-Ratelimit-Remaining`, `-Concurrency-Remaining`.

## Control-plane (/api/control/v1, /api/auth/*, /api/webhooks/*)

### Auth & editions

| HTTP | code | Meaning |
|---|---|---|
| 401 | `unauthorized` | No/invalid session where one is required |
| 401 | `invalid_credentials` | Login: unknown email or wrong password (identical shape) |
| 403 | `email_unverified` | Login before email verification |
| 403 | `account_disabled` / `sso_user_disabled` | Suspended account |
| 403 | `feature_not_licensed` | Enterprise feature without license (sso, orgs, audit_export…) |
| 403 | `ip_not_allowed` | Client IP (X-Forwarded-For) outside the configured ipacl allowlist |
| 400 | `invalid_cidrs` | ipacl payload is not an array of valid IPv4 CIDRs / literal IPs |
| 403 | `hosted_only` | Self-serve accounts on plain OSS |
| 403 | `sso_email_missing` / `sso_email_unverified` | IdP claims insufficient |
| 400 | `invalid_verification_token` | Expired/used/unknown verification token |

### SSO/OIDC

`gateway_identity_missing/_mismatch` (401/403) · `sso_state_mismatch` /
`sso_state_expired` (401) · `sso_not_configured` (409) ·
`sso_token_exchange_failed` / `sso_token_missing` (401) ·
`sso_provider_error` (401) · `sso_not_configured` also on start.

### Config & publishing

| HTTP | code | Meaning |
|---|---|---|
| 409 | `insufficient_active_gateways` | Publish needs live gateways |
| 426 | `upgrade_required` | Gateway too old for schema v2 |
| 409 | `ack_version_not_published` / `ack_identity_mismatch` / `gateway_capability_mismatch` | Ack does not match published state |
| 404 | `config_version_not_found` | Version unknown/rolled back source invalid |
| 409 | `model_alias_conflict` / `model_unavailable` / `invalid_model_strategy` / `invalid_model_alias` | Model route validation |
| 400 | `provider_model_missing_slug` / `provider_operation_invalid_response` / `provider_operation_response_too_large` | Provider operations |

### Billing & webhooks (money path — all idempotent)

| HTTP | code | Meaning |
|---|---|---|
| 200 | `credited:false` | Webhook replay acknowledged, no double credit |
| 401 | `invalid_signature` | Paddle HMAC mismatch/stale ts |
| 422 | `unsupported_currency` / `transaction_not_paid` / `missing_team_reference` / `invalid_amount` | Payable-check failed |
| 422 | `invalid_amount` (adjust) | Manual adjustment ±0 or >10k |
| 503 | `webhook_not_configured` | PADDLE_WEBHOOK_SECRET unset |

### Codex quota & CAS

`quota_reset_credit_unavailable` · `quota_reset_confirmation_required` ·
`quota_reset_pending` · `quota_reset_active` · `quota_reset_state_conflict` ·
`quota_reset_not_unknown` · `quota_reset_operation_not_found` ·
`resolution_invalid` / `resolution_note_required` ·
`codex_account_not_found` · `credential_revision conflict → 409
credential_revision_conflict`.

### Misc

`invalid_content_length` / `payload_too_large` (413) · `invalid_json`
(400) · `validation_failed` (400, Zod issues attached) · `not_found`
(404) · `internal_error` (500; details only with CODEX_TEST_MODE=true).

## Adding an error

1. Reuse an existing code when the meaning matches.
2. New code = lowercase snake_case, added here in the same commit.
3. Money-path codes must keep their idempotency guarantees (see
   credit_transactions UNIQUE + webhook tests).
