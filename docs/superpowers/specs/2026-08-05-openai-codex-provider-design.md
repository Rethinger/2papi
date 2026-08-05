# OpenAI Codex Provider, Model Discovery, and Quota Management Design

Date: 2026-08-05
Status: Draft for written user review
Related designs:

- `docs/superpowers/specs/2026-08-04-multi-account-ai-gateway-design.md`
- `docs/superpowers/specs/2026-08-04-dashboard-control-plane-design.md`
- `docs/superpowers/specs/2026-08-05-dashboard-ru-en-localization-design.md`

## 1. Goal

Add an `openai-codex` provider to 2papi so a local administrator can sign in with a ChatGPT Codex account, discover the models available to that account, publish selected models under user-defined public aliases, use those aliases through the gateway, inspect the account plan and quota, and consume an available Codex rate-limit reset credit safely.

The implementation must preserve the current product rules:

- PostgreSQL and Redis stay outside the gateway request hot path.
- Configuration remains draft-first and reaches the gateway only after publish.
- The gateway adopts complete immutable snapshots atomically.
- PostgreSQL and Redis are not published to the host.
- Credentials are envelope-encrypted at rest.
- The gateway continues to support a local YAML fallback when the control plane is unavailable.
- Russian and English dashboard locales remain complete and visually verified.

## 2. Confirmed product choices

- Provider identity: `openai-codex`.
- Primary authentication: browser OAuth link with PKCE and automatic localhost callback.
- Fallback authentication: Device Code.
- Additional authentication: import an existing `~/.codex/auth.json` file.
- Account name is optional. The default is the verified account email, then ChatGPT account ID, then a generated local name.
- Account cards show email or chosen display name, ChatGPT plan, token state, quota windows, reset time, and reset-credit availability.
- Model discovery can target all discoverable accounts, one provider, or one account.
- Discovery never publishes configuration automatically.
- The user selects models from discovery results and assigns the exact public model name exposed by 2papi.
- The public name appears in `GET /v1/models`, is accepted in requests, and is returned in downstream response model fields.
- Missing upstream models are marked unavailable. They are not silently deleted from published configuration.
- Quota refresh uses the ChatGPT Codex usage JSON contract when available.
- Reset consumes a real, non-refundable rate-limit reset credit and therefore requires a fresh capability check and explicit confirmation.
- If the usage or reset-credit backend is unavailable, the UI offers a link to the official Codex Usage page instead of pretending that reset succeeded.

## 3. Current-state gaps

The current repository cannot add Codex by changing only account data:

- `internal/server/server.go` exposes only `/v1/models` and `/v1/chat/completions`.
- `internal/proxy/proxy.go` always forwards to `/v1/chat/completions` with a static API key.
- `internal/config/config.go` accounts contain only `base_url` and `api_key` credential fields.
- `internal/adapter/openai/openai.go` is only a path constant, not an adapter seam.
- `control-plane/lib/control.ts` accepts only `{api_key}` credentials.
- `control-plane/lib/api.ts` compares the internal service token with ordinary string equality and must move to a length-safe constant-time comparison before adding credential mutation interfaces.
- The current `config_versions.snapshot` stores the compiled runtime snapshot, including decrypted API keys. This conflicts with the intended envelope-encryption guarantee and must be corrected before adding longer-lived OAuth refresh tokens.
- There is no durable discovered-model inventory, OAuth session state, quota snapshot, or idempotent provider-operation record.

Codex also uses a different upstream protocol:

- Backend base: `https://chatgpt.com/backend-api/codex`.
- Model discovery: `GET /models?client_version=...`.
- Inference: Responses protocol at `/responses` with SSE for streaming.
- Authentication: OAuth bearer token plus `ChatGPT-Account-ID`.
- Quota and reset-credit endpoints are under `https://chatgpt.com/backend-api/wham`, not the standard OpenAI Platform API.

## 4. Architecture

### 4.1 Components

- **Next.js control plane:** OAuth orchestration, Device Code orchestration, auth-file import, account persistence, model-discovery orchestration, draft creation, quota UI, reset confirmation, audit, and runtime-state persistence.
- **PostgreSQL:** provider and account configuration, encrypted credential bundles, credential revisions, discovered models, quota snapshots, reset-credit snapshots, idempotent provider operations, configuration versions, and audit events.
- **Redis:** one-time OAuth and Device Code sessions, short-lived operation state, publish notifications, credential-revision notifications, and gateway acknowledgements.
- **Go gateway public server:** `/v1/models`, `/v1/responses`, `/v1/chat/completions`, virtual-key authentication, routing, retries, response translation, and SSE streaming.
- **Go gateway internal server:** provider operations requested by the control plane, including model discovery, quota refresh, and reset-credit consumption. It is private to the Compose network and is not host-published.
- **Provider adapter module:** hides provider authentication, upstream URL construction, request and response conversion, model discovery, usage parsing, and credential refresh behind a small gateway interface.

### 4.2 Deep provider adapter module

Two concrete adapters make this a real seam:

- `openai-compatible`
- `openai-codex`

The external interface should remain small:

```go
type Adapter interface {
    Execute(context.Context, Execution) (*Result, error)
    Operate(context.Context, Operation) (OperationResult, error)
}
```

`Execute` covers latency-sensitive inference. Its request identifies the downstream endpoint, selected account, public alias, upstream model, headers, and bounded body. Its result exposes status, safe response headers, and a stream body.

`Operate` covers control-plane operations such as `discover_models`, `read_usage`, and `consume_reset_credit`. The operation is a tagged request and the result is a tagged response. Callers do not learn Codex URLs, OAuth headers, JSON shapes, or refresh rules.

The Codex adapter implementation owns internal seams for:

- OAuth credential refresh
- request conversion
- SSE conversion
- model-list parsing
- quota parsing
- reset-credit handling

These internal seams are available to adapter tests but are not exported to server or routing callers.

### 4.3 Hot-path and operations rule

The public gateway request path never reads PostgreSQL or Redis. It uses the active immutable runtime snapshot and adapter-local in-memory credential state.

Control-plane operations are sent to the gateway internal server so Codex protocol knowledge and token refresh logic are not duplicated in TypeScript. An operation may include an unpublished account configuration and credential bundle, allowing model discovery before the account is published.

The internal operation endpoint:

- listens on a separate container-only port
- requires the internal service token
- accepts only known operation types
- applies strict body limits and timeouts
- never logs credentials or raw upstream bodies
- returns a rotated credential bundle when an operation refreshed the token

The control plane persists returned credentials immediately using compare-and-swap credential revision rules.

## 5. Authentication flows

### 5.1 Browser OAuth link

Browser OAuth is the default and fastest path.

1. The Accounts screen opens the Codex account modal.
2. The user optionally enters a local account name and routing defaults.
3. `POST /api/control/v1/codex/oauth/start` creates a one-time session containing state, nonce, PKCE verifier, pending account options, and creation time.
4. The session is stored in Redis with a maximum 30-minute TTL. Only a random session identifier is returned to the browser.
5. The UI opens the returned `https://auth.openai.com/oauth/authorize` URL.
6. The OAuth redirect URI is fixed to `http://localhost:1455/auth/callback`, matching the Codex CLI-compatible public client.
7. Compose adds `127.0.0.1:1455:3000` alongside the existing `127.0.0.1:13000:3000` mapping, so only the local machine can reach the callback.
8. `/auth/callback` validates the direct request host and callback path, ignores forwarded host headers unless an explicitly configured trusted proxy is enabled, atomically consumes state, exchanges the code server-side, verifies the ID token, stores the encrypted credential bundle, creates the account, and redirects to the configured `DASHBOARD_PUBLIC_URL`, which defaults to `http://localhost:13000`.
9. No access token, refresh token, authorization code, or ID token is placed in a redirect URL, browser storage, or rendered page.

The redirect URI is not accepted from arbitrary user input. The production adapter pins the OpenAI authorization and token origins. Test overrides are available only through explicit test configuration.

If port 1455 is unavailable, the modal explains the problem and offers Device Code and auth-file import.

### 5.2 Device Code

Device Code is a beta fallback aligned with the current Codex login flow.

1. `POST /api/control/v1/codex/device/start` starts the upstream device flow.
2. The response contains a user code, verification URL, expiration, and local session ID.
3. The UI displays the code, provides copy and open-link buttons, and polls only the local status endpoint.
4. `GET /api/control/v1/codex/device/:session/status` performs at most one due upstream poll and returns `pending`, `slow_down`, `expired`, `denied`, `failed`, or `complete`.
5. On completion, the control plane validates the returned tokens and creates the account using the same persistence path as browser OAuth.

Polling intervals and `slow_down` responses are honored. Sessions are single-use and expire automatically.

### 5.3 Import `auth.json`

The import modal accepts a local JSON file with a strict size limit. It supports the current official Codex auth-cache shape and explicitly documented compatible legacy shapes.

The server:

- parses JSON with a strict schema
- rejects unknown top-level credential containers unless explicitly supported
- validates token shape and required account identity
- refreshes an expired access token when a refresh token is present
- verifies the ID token before trusting email, plan, or account identifiers
- stores only the normalized encrypted credential bundle
- never stores the uploaded file or raw JSON
- reports secret presence without returning secret values

Import is rejected if the file contains only an API key and no Codex account credential. Standard OpenAI API-key accounts remain handled by the existing OpenAI-compatible provider.

### 5.4 Token and identity validation

The normalized OAuth credential bundle contains:

- `access_token`
- `refresh_token`, when issued
- `id_token`, when issued
- `expires_at`
- `client_id`
- `chatgpt_account_id`
- `chatgpt_user_id`, when present
- `email`, when verified
- `plan_type`, when verified
- `auth_method`

ID-token validation uses OpenAI JWKS with cached key rotation and verifies issuer, audience, signature, expiration, and nonce where applicable. Unverified JWT payload data may be used only as a diagnostic hint, never as an authorization or account-binding decision.

The UI maps known plan values to localized labels and shows unknown values as technical data without altering them.

## 6. Credential lifecycle and snapshot security

### 6.1 No plaintext credentials at rest in config versions

`config_versions.snapshot` becomes credential-free. It stores account IDs, adapter identity, credential revision, routing fields, public model aliases, and other declarative configuration, but not API keys, access tokens, refresh tokens, or ID tokens.

The internal snapshot endpoint materializes a runtime snapshot only when an authorized gateway requests it:

1. Load the published credential-free config version.
2. Resolve each account's current encrypted secret record.
3. Decrypt credentials in memory.
4. Build the runtime snapshot.
5. Compute a runtime checksum over the complete canonical payload.
6. Return the payload without writing it back to PostgreSQL or Redis.

Logs and error payloads must never include the materialized runtime snapshot.

A one-time migration sanitizes every existing `config_versions` row before Codex OAuth credentials can be stored. It recompiles credential-free declarative snapshots from the source tables while preserving version status and rollback lineage. Rows that cannot be reconstructed are marked invalid and cannot be republished. Because old plaintext API keys may remain in PostgreSQL WAL, backups, or previously copied database files, the migration documentation requires rotating existing upstream API keys after upgrade when disk or backup exposure is in scope.

### 6.2 Config version and credential revision

Configuration and credential changes have separate identities:

- `config_version` changes only through draft publish or rollback.
- `credential_revision` increments whenever an account credential bundle changes.
- `credential_digest` is derived from account IDs and credential revisions, never secret bytes.
- `runtime_checksum` covers the materialized runtime payload sent to a gateway.

A gateway re-adopts when either the published configuration or credential digest changes. An unchanged tuple is skipped and does not generate duplicate acknowledgements.

Gateway acknowledgements record config version, credential digest, runtime checksum, and adopted or rejected status.

The version 2 snapshot wire envelope is explicit:

```json
{
  "config_version": 42,
  "schema_version": 2,
  "config_checksum": "sha256:...",
  "credential_digest": "sha256:...",
  "runtime_checksum": "sha256:...",
  "snapshot": {}
}
```

`config_version` is the PostgreSQL configuration-history identity. `schema_version` is the inner gateway configuration schema. They must not share the ambiguous name `version` in new code. During migration, the client accepts the old outer `version` field only for legacy envelopes. The runtime checksum is calculated over one specified canonical JSON byte serialization of `snapshot`, exactly shared by TypeScript and Go through golden fixtures. An acknowledgement echoes all five identity fields supported by the sender, so the control plane can distinguish a configuration change from a credential-only refresh.

### 6.3 Refresh ownership

The Codex adapter owns refresh execution for both inference and provider operations.

- Refresh begins five minutes before expiry.
- A per-account singleflight prevents concurrent refresh storms.
- A successful response updates adapter-local state immediately.
- The gateway persists the rotated bundle through `PUT /api/internal/v1/accounts/:id/credentials` with the expected credential revision.
- The control plane writes a new encrypted secret record, swaps the account reference, increments the revision, and emits a credential notification without creating a configuration draft.
- A revision conflict causes the gateway to fetch the latest runtime snapshot instead of overwriting newer credentials.
- If persistence is temporarily unavailable, the gateway keeps the refreshed bundle in memory and retries with bounded backoff.

A gateway restart before an unpersisted rotating refresh token is saved can require reauthentication. The UI must expose this as a degraded credential-persistence state rather than claiming the account is healthy.

### 6.4 Backward compatibility

Gateway config schema version 2 adds adapter and structured credential fields. The gateway continues to accept version 1 YAML and legacy runtime snapshots during rollout.

The snapshot endpoint is a dual-format bridge:

- a gateway sends `X-Gateway-Snapshot-Schemas: 1,2` and `X-Gateway-Envelope-Version: 2` when it supports the new contract
- a request without those headers receives a materialized schema version 1 snapshot in the legacy `{version, checksum, snapshot}` envelope
- a capable gateway receives the explicit version 2 envelope from section 6.2
- both responses are materialized from credential-free configuration history, so serving a legacy gateway never reintroduces plaintext credentials at rest
- acknowledgements persist advertised capabilities and the envelope actually adopted

At startup, if the control plane has no usable published snapshot, the gateway starts from the validated YAML file. After a control-plane snapshot has been adopted, a later control-plane outage keeps the last adopted runtime snapshot active. The gateway never silently switches a running process back to an older YAML configuration.

Codex accounts and schema version 2-only fields remain blocked until every currently active gateway has acknowledged schema version 2. A gateway is active when it has polled or acknowledged within a configurable capability TTL, defaulting to three poll intervals. Advertised headers are recorded as observations, but publish gating and `426 upgrade_required` decisions use the persisted gateway capability state, not an untrusted request header alone. A stale or unknown active capability produces a publish validation error rather than a partially usable snapshot. After version 2-only configuration is published, a later legacy gateway request receives `426 upgrade_required`, never a lossy schema version 1 snapshot.

## 7. Data model

A new migration extends the existing schema.

### 7.1 Providers and accounts

`providers`:

- keep `adapter` as the adapter discriminator
- use `openai-codex` for the new provider
- pin the production upstream origin in adapter code
- allow fake upstream override only under test configuration

`accounts` additions:

- `auth_type text NOT NULL DEFAULT 'api_key'`
- `credential_revision bigint NOT NULL DEFAULT 1`
- `external_account_id text`
- `account_email text`
- `plan_type text`
- `subscription_expires_at timestamptz`
- `token_expires_at timestamptz`
- `last_credential_refresh_at timestamptz`
- `credential_persistence_status text`

Secrets remain in `secret_records`. Replaced records are retained only according to a bounded rotation-retention policy and are never returned through management reads.

### 7.2 Discovered models

Add `discovered_models`:

- `id uuid primary key`
- `provider_id uuid`
- `account_id uuid`
- `upstream_model text`
- `display_name text`
- `capabilities jsonb`
- `visibility text`
- `supported_in_api boolean`
- `available boolean`
- `raw_metadata jsonb` with secret-field filtering and a size limit
- `first_seen_at timestamptz`
- `last_seen_at timestamptz`
- unique `(account_id, upstream_model)`

A discovery run upserts returned models and marks previously seen but now missing models unavailable. It never deletes model aliases or account mappings.

### 7.3 Quota state

Add `account_provider_state` with one row per account:

- normalized quota JSON
- reset-credit JSON containing only count and expiration metadata
- quota capability status
- fetched timestamp
- last successful provider operation
- last provider error code and safe message

Access tokens, upstream reset-credit IDs, and raw error bodies are forbidden in this table.

### 7.4 Idempotent provider operations

Add `provider_operations`:

- operation ID and client idempotency key
- account ID
- operation type
- status: `pending`, `succeeded`, `failed`, or `unknown`
- lease expiration and heartbeat timestamp for pending work
- preflight quota and reset-credit summary
- upstream request ID
- safe result summary
- warning code
- resolution source: `upstream`, `reconciled`, or `manual`
- resolver, resolution note, and resolution timestamp when manually resolved
- created, started, and completed timestamps
- unique `(account_id, operation_type, idempotency_key)`

Reset-credit operations use this table to prevent a browser retry from spending a second credit.

### 7.5 Public aliases

The existing `model_aliases.alias` remains the public model identifier. Add a case-folded uniqueness constraint so aliases that differ only by case cannot create ambiguous client behavior.

Allowed aliases:

- length 1 to 128
- characters `A-Z`, `a-z`, `0-9`, `.`, `_`, `:`, `/`, and `-`
- no whitespace, control characters, or leading/trailing separators

The exact stored alias is returned to clients. Request lookup and virtual-key allowlist matching are exact and case-sensitive. The case-folded unique constraint only prevents confusing aliases such as `Luna-Code` and `luna-code` from coexisting. It does not make lookup case-insensitive. The gateway rewrites upstream response model fields back to the exact public alias.

Renaming an existing published alias is a distinct explicit operation. In one transaction it updates the alias, every `virtual_keys.models` reference, dependent mappings, and audit metadata before compiling a draft. If any dependent reference cannot be migrated, the rename is rejected. Discovery conflict actions never perform an implicit rename.

## 8. Control-plane interfaces

### 8.1 Authentication

- `POST /api/control/v1/codex/oauth/start`
- `GET /auth/callback`
- `POST /api/control/v1/codex/device/start`
- `GET /api/control/v1/codex/device/:session/status`
- `POST /api/control/v1/codex/import-auth`
- `POST /api/control/v1/accounts/:id/reauthorize`

All account-creation paths converge on one normalization, validation, encryption, audit, and draft-generation implementation.

### 8.2 Discovery

- `POST /api/control/v1/model-discovery`

Request scope:

- `all`
- `provider_id`
- `account_id`

The response reports per-account success, unsupported capability, or safe failure. Partial success is preserved.

- `GET /api/control/v1/discovered-models` lists filterable persisted results.
- `POST /api/control/v1/models/import-selection` accepts selected discovery rows, public aliases, and selected account mappings. It validates collisions and creates a new draft.

### 8.3 Quota

- `POST /api/control/v1/accounts/:id/quota/refresh`
- `POST /api/control/v1/accounts/:id/quota/reset`
- `POST /api/control/v1/accounts/:id/quota/reset/:operation_id/reconcile`
- `POST /api/control/v1/accounts/:id/quota/reset/:operation_id/resolve`
- `GET /api/control/v1/accounts/:id/quota`

Refresh is a POST because it persists runtime state and audit information.

Reset requires:

- a client idempotency key
- explicit confirmation from the UI
- a live preflight showing at least one available, unexpired reset credit
- an eligible parent Codex account

### 8.4 Internal gateway interfaces

Control plane to gateway:

- `POST /internal/v1/provider-operations`

Gateway to control plane:

- `PUT /api/internal/v1/accounts/:id/credentials`
- existing snapshot and acknowledgement interfaces extended with credential digest fields

These interfaces use the existing internal service token, strict method and content-type checks, length-safe constant-time token comparison, and non-public Compose networking. Provider-operation requests are limited to 2 MiB, credential updates to 256 KiB, auth-file imports to 1 MiB, and runtime snapshots to 16 MiB. Oversized bodies are rejected before JSON parsing. Operation timeouts are explicit per operation type and do not inherit an unbounded browser connection lifetime.

## 9. Model discovery and selection behavior

### 9.1 Fetch menu

The Models screen adds a `Fetch models` menu:

- all providers
- one provider
- one account

The action runs with bounded concurrency and shows progress per account. One failed provider does not discard successful results from other providers.

For `openai-compatible`, discovery uses `GET {base_url}/v1/models` when supported.

For `openai-codex`, discovery uses the current Codex models contract and records model slug, visibility, API support, context metadata, and supported features. Models not supported for API use remain visible as unavailable and cannot be selected by default.

### 9.2 Selection table

The discovery result table provides:

- selection checkbox
- upstream model slug
- provider and accounts where it was found
- capability badges
- availability status
- editable public alias
- alias collision validation
- account-mapping selection

When the same upstream slug exists on multiple compatible accounts, the UI groups it and selects all eligible accounts by default. The user may remove accounts before import.

### 9.3 Draft behavior

`Import selected` creates or updates model aliases and mappings in the current source tables, then compiles a new draft. It does not publish.

Existing aliases are updated only after an explicit conflict decision:

- skip
- replace upstream mapping
- add newly discovered accounts to the existing mapping

The publish screen summarizes added aliases, removed mappings, renamed public models, unavailable upstream models, and validation errors.

### 9.4 Gateway-visible names

If upstream model `gpt-5.6-codex` is published as `luna-code`:

- `/v1/models` contains `luna-code`
- clients request `model: "luna-code"`
- routing resolves the selected account pool
- the Codex adapter sends `gpt-5.6-codex` upstream
- non-streaming and streaming responses expose `luna-code` back to the client
- audit and usage records preserve both public alias and upstream slug

## 10. Gateway protocol support

### 10.1 Public endpoints

The gateway exposes:

- `GET /v1/models`
- `POST /v1/responses`
- `POST /v1/chat/completions`

Virtual-key authentication, model allowlists, RPM limits, routing, cooldown, and circuit behavior apply to both inference endpoints.

### 10.2 Native Responses path

For `/v1/responses`, the Codex adapter:

- validates and rewrites the public alias to the upstream model
- preserves supported Responses fields
- sets Codex authentication and account headers
- sends to the pinned Codex backend
- streams SSE without buffering the complete response
- rewrites model identifiers and removes unsafe upstream headers
- records quota signals from response headers and `codex.rate_limits` events when present

Unsupported fields fail with a structured 400 response. They are not silently discarded.

### 10.3 Chat Completions compatibility

For `/v1/chat/completions`, the Codex adapter translates between Chat Completions and Responses.

Initial supported compatibility surface:

- system, developer, user, assistant, and tool messages
- text content
- streaming and non-streaming output
- function tools and tool choice
- tool calls and tool results
- temperature and output-token limits only when supported by the selected model
- reasoning effort when supplied through the supported OpenAI-compatible field
- stop behavior when representable

The response translator emits standard `chat.completion` and `chat.completion.chunk` shapes, including tool-call deltas and a final finish reason.

Image input, structured output, or other fields are supported only when discovery reports the capability and an explicit converter exists. Otherwise the gateway returns a clear unsupported-feature error.

### 10.4 Retry and stream safety

Retries and account fallback are allowed only before downstream headers or SSE data are committed. After the first client-visible byte, the gateway never switches accounts.

A refresh-token retry is separate from routing retry and occurs at most once for an authentication failure that is known to be refreshable.

## 11. Quota and reset-credit behavior

### 11.1 Usage refresh

The Codex adapter normalizes the usage response into:

- account and user identifiers
- verified plan type
- primary rate-limit window
- secondary rate-limit window
- additional named limits such as model-family limits
- used percent
- reset time and remaining duration
- credits metadata when present
- fetched timestamp

The current observed endpoints are:

- `GET https://chatgpt.com/backend-api/wham/usage`
- `GET https://chatgpt.com/backend-api/wham/rate-limit-reset-credits`

These are not documented as a stable public OpenAI API. The adapter treats them as a capability that can become unavailable. A shape change produces `quota_contract_changed`, preserves the last known snapshot as stale, and offers the Codex Usage fallback.

The implementation uses a normal standards-compliant HTTP transport. It does not add browser fingerprint impersonation or Cloudflare-bypass behavior.

### 11.2 Reset preflight

The reset button is enabled only when a fresh read confirms an available, unexpired `codex_rate_limits` reset credit. Cached data may display the last known count but cannot authorize a reset.

The confirmation dialog states:

- one reset credit will be consumed
- the credit is non-refundable
- the number of currently available credits
- the expiration time of the next credit when known

### 11.3 Reset execution

The observed reset contract is:

- `POST https://chatgpt.com/backend-api/wham/rate-limit-reset-credits/consume`
- JSON body: `{ "redeem_request_id": "<uuid>" }`

The server-generated redeem request ID is stored before the upstream request and reused for the same idempotency key.

Reset uses a durable state machine:

1. Acquire a per-account database advisory lock.
2. Return the saved result for an existing succeeded idempotency key.
3. Return `202 pending` for the same active request.
4. Reject a different reset while the account has a pending or unknown reset operation.
5. Perform the fresh credit preflight.
6. Insert or resume `pending` with a lease, heartbeat, preflight snapshot, and redeem request ID before contacting upstream.
7. Move only through `pending -> succeeded`, `pending -> failed`, or `pending -> unknown`.
8. If the process dies or the lease expires after dispatch may have happened, move to `unknown`, never directly to `failed`.

A partial unique constraint permits at most one `pending` or `unknown` reset operation per account.

After upstream success, the operation continues even if the browser disconnects:

1. Mark the operation succeeded with the safe upstream result.
2. Clear gateway-local rate-limit and circuit state for the account without changing the user's manual enabled switch.
3. Refresh usage and reset-credit count.
4. Persist the normalized runtime state.
5. Return the updated account and quota when the client is still connected.

If reset succeeded but post-processing failed, the response remains a success with a warning code. It must not invite an automatic retry that could consume another credit.

If the outcome is unknown due to a transport interruption after the request may have reached upstream, the operation becomes `unknown`. Unknown state blocks every later automated reset for that account. The server performs reconciliation by comparing the stored preflight snapshot with a fresh upstream credit-detail and usage read. It marks the operation succeeded only when upstream evidence conclusively identifies the credit as redeemed or the relevant windows as reset by this operation. It marks the operation failed only when upstream evidence conclusively proves the request was not consumed.

If the upstream contract cannot prove either outcome, the operation stays unknown and the UI opens Codex Usage for manual verification. Manual resolution requires a second confirmation, a non-empty note, and a dedicated audit event. It records the administrator decision and never sends an upstream reset request. Resolving an operation does not itself authorize another spend. Any later reset still requires a new idempotency key and a new live reset-credit preflight. The original reset request is never retried automatically.

### 11.4 Fallback

When quota capability is unsupported, blocked, or changed:

- the account remains usable for inference if inference works
- the last quota snapshot is labeled stale
- the reset button is disabled
- an `Open Codex Usage` action opens the official usage page in a new tab
- no reset is simulated locally

## 12. Dashboard design

### 12.1 Add-account modal

The Codex provider modal has three tabs:

- `Sign in with browser`
- `Device Code`
- `Import auth.json`

Each tab shares optional account name and routing defaults. Switching tabs does not leak values from the selected auth method.

Browser sign-in shows callback-port readiness and explains Device Code fallback. Device Code shows copyable code, expiration, and polling state. Import shows the selected file name and validation outcome, never token content.

### 12.2 Account cards

Codex account cards show:

- user-selected display name
- email when available
- localized plan label
- token status and expiry
- authentication method
- last credential refresh
- quota windows with percent and reset time
- reset-credit count and expiration
- stale or unsupported quota state
- refresh quota, reset quota, reauthorize, and open Usage actions

Known states and actions are localized. Provider model slugs, account IDs, audit payloads, and unknown plan strings remain technical data.

### 12.3 Models screen

The Models screen adds the Fetch menu, progress summary, discovery table, editable public aliases, account mapping selection, conflict resolution, and draft summary.

Keyboard navigation, focus containment, Escape close, visible focus, screen-reader labels, mobile wrapping, and no-horizontal-overflow requirements match the localization design.

## 13. Error handling and reliability

Stable error codes include:

- `codex_oauth_state_invalid`
- `codex_oauth_callback_port_unavailable`
- `codex_device_pending`
- `codex_device_expired`
- `codex_auth_file_invalid`
- `codex_token_refresh_failed`
- `codex_credential_revision_conflict`
- `codex_model_discovery_unsupported`
- `codex_model_contract_changed`
- `codex_quota_unsupported`
- `codex_quota_contract_changed`
- `codex_reset_credit_unavailable`
- `codex_reset_outcome_unknown`
- `codex_feature_unsupported`

User-facing messages are localized and do not include tokens, raw upstream bodies, JWTs, or credential-bearing URLs.

Model discovery and quota refresh preserve partial success. Draft compilation remains all-or-nothing. Runtime snapshot adoption remains all-or-nothing.

## 14. Security requirements

- Pin Codex OAuth and backend origins in production code.
- Reject custom Codex base URLs from management input.
- Use PKCE S256, cryptographic state, nonce, one-time sessions, and fixed callback URI.
- Verify ID-token signature and claims through JWKS.
- Keep OAuth sessions in Redis with TTL and atomic consume.
- Never store OAuth tokens in browser storage, query parameters, audit payloads, discovered-model metadata, quota tables, or config-version snapshots.
- Envelope-encrypt normalized credentials with per-record data keys.
- Redact authorization, cookie, token, code, verifier, assertion, and account-secret fields from logs.
- Apply strict upload and JSON depth limits to auth-file import.
- Require the internal service token on gateway operation and credential-update interfaces.
- Keep the gateway internal port, PostgreSQL, and Redis unexposed to the host.
- Use constant-time internal-token comparison.
- Use idempotency records and explicit confirmation for reset credits.
- Do not retry a reset automatically after an unknown outcome.
- Do not log prompts or response bodies by default.

## 15. Audit and observability

Audit actions:

- `codex.oauth.start`
- `codex.oauth.complete`
- `codex.device.start`
- `codex.device.complete`
- `codex.auth.import`
- `codex.account.reauthorize`
- `credential.refresh`
- `model.discovery.run`
- `model.discovery.import`
- `quota.refresh`
- `quota.reset.requested`
- `quota.reset.completed`
- `quota.reset.unknown`

Audit payloads contain resource IDs, counts, scopes, safe status codes, public aliases, and credential revisions. They never contain credentials or raw upstream payloads.

Metrics include provider-operation duration, token refresh outcomes, discovery counts, quota capability state, reset outcomes, adapter errors, response-conversion errors, and runtime credential-persistence backlog.

## 16. Rollout

1. Ship a gateway that still consumes the legacy control-plane envelope but also advertises and accepts the version 2 envelope and config schema.
2. Add the control-plane dual-format snapshot endpoint, capability-aware acknowledgements, and credential-free runtime materialization.
3. Sanitize historical config versions, enable the credential revision digest, and verify that active gateways can still adopt materialized schema version 1 snapshots.
4. Wait for every active gateway to acknowledge version 2 capability, then allow schema version 2 publication.
5. Add the private gateway operation server and credential CAS persistence.
6. Add Codex OAuth, Device Code, and auth-file import.
7. Add Codex inference through `/v1/responses` and Chat Completions compatibility.
8. Add discovery storage, selection, alias editing, and draft import.
9. Add quota refresh, reset credits, idempotent reset, reconciliation, and Usage fallback.
10. Add RU/EN UI and full validation.
11. Enable `openai-codex` in the Compose seed only after gateway capability acknowledgement.

Rollback of a published model configuration does not roll back operational OAuth token refreshes or quota state. Credential rotations are operational security state, not configuration history.

## 17. Validation

### 17.1 Control-plane tests

- OAuth state, nonce, PKCE, callback consume, fixed redirect, direct-host validation, spoofed forwarded-host rejection, callback port mapping, and open-redirect rejection
- Device Code polling intervals, expiration, denial, and completion
- auth-file accepted and rejected shapes, size limits, and no raw persistence
- JWKS signature and claim validation
- envelope encryption and credential rotation CAS
- proof that all historical and new `config_versions.snapshot` rows contain no plaintext credentials
- migration behavior for unreconstructable historical versions and rotation guidance for old keys
- discovery upsert, missing-model marking, partial failure, and alias conflicts
- exact public alias preservation and case-fold collision rejection
- quota normalization, stale state, contract-change state, and reset-credit expiration
- reset idempotency, database locking, lease expiry to unknown, unknown-outcome server reconciliation, strongly audited manual-resolution gate, mandatory new preflight after resolution, post-success warning, and no double consumption
- request body limits for auth import, provider operations, credential updates, and runtime snapshots
- exact case-sensitive alias lookup, case-fold collision rejection, and transactional virtual-key allowlist migration on rename
- constant-time internal service-token comparison for equal-length and unequal-length inputs
- audit redaction
- migrations and PostgreSQL constraints

### 17.2 Gateway tests

- adapter selection for OpenAI-compatible and Codex accounts
- schema version 1 and 2 loading, including legacy and explicit version 2 wire envelopes
- capability advertisement, legacy materialization, active-gateway gating, and dual-format rollout
- exact differentiation of config version, schema version, config checksum, credential digest, and runtime checksum
- shared TypeScript and Go canonical-JSON golden fixtures that produce the same runtime checksum
- persisted capability-state gating and `426 upgrade_required` behavior that cannot be enabled by request headers alone
- runtime checksum changes on credential revision without config-version changes
- unchanged runtime snapshot adoption remains idempotent
- token refresh singleflight and persistence retry
- credential revision conflict handling
- native Responses non-stream and SSE
- Chat Completions conversion for text and tool calls
- public alias rewrite in request and response
- unsupported feature errors
- no retry after stream commitment
- quota header and `codex.rate_limits` observation
- race detector and `go vet`

### 17.3 Compose E2E

A fake Codex upstream provides OAuth, Device Code, token refresh, models, responses, usage, reset-credit listing, and reset consumption.

The scripted flow verifies:

1. Start the stack without published database ports, with the callback bound only at `127.0.0.1:1455` and the gateway operation port available only inside Compose.
2. Create a Codex account through a test OAuth callback or auth-file fixture.
3. Fetch models for one account and for all providers.
4. Select an upstream model and rename it to `luna-code`.
5. Confirm the change exists only in a draft.
6. Publish and wait for gateway adoption.
7. Confirm `/v1/models` contains exactly `luna-code`.
8. Send authenticated non-streaming and SSE requests through `/v1/responses`.
9. Send authenticated non-streaming and SSE requests through `/v1/chat/completions`.
10. Confirm responses expose `luna-code`, while fake upstream received the original slug.
11. Refresh quota and display plan and reset windows.
12. Consume one reset credit with an idempotency key.
13. Retry the same client request and prove only one upstream credit was consumed.
14. Simulate a reset transport interruption, prove the operation becomes unknown, and prove later reset attempts remain blocked until reconciliation or explicit manual resolution.
15. Confirm quota refresh and cleared account cooldown state.
16. Roll back the published config and confirm routing returns to the previous alias set.
17. Stop the control plane and confirm the gateway continues using its last adopted runtime snapshot. YAML fallback remains independently verified.

### 17.4 Playwright visual proof

Validate RU and EN at desktop and mobile widths:

- all three authentication tabs
- browser OAuth success and error return states
- Device Code copy, pending, expired, and complete states
- auth-file validation
- account plan and quota cards
- reset confirmation and warning states
- Fetch models menu
- partial discovery progress
- model selection and alias editing
- collision errors
- draft summary
- Escape dialog close
- `<html lang>` persistence
- no console errors
- no horizontal overflow
- screenshots for all new views and modals

### 17.5 Optional live smoke

A manual, opt-in live test may use the administrator's own Codex account to verify current upstream compatibility. It is never part of automated CI, never records tokens, and never consumes a reset credit without a separate explicit confirmation.

## 18. Acceptance criteria

The feature is complete only when:

- all three login methods create a usable encrypted Codex account
- browser OAuth returns automatically through localhost port 1455
- the account card shows a verified plan and token state
- model fetch works for one account and all providers
- selected models can be renamed before draft import
- the exact public names appear in `/v1/models` and work through both inference endpoints
- published aliases reach the gateway only after publish
- quota refresh shows current windows or a clear unsupported state
- reset consumes at most one credit per idempotency key and refreshes state afterward
- no config-version row, management response, audit event, or log contains plaintext credentials
- internal authentication is constant-time and the gateway operation port is not host-published
- existing OpenAI-compatible routing and YAML fallback remain green
- Node tests, PostgreSQL integration tests, TypeScript build, Go tests with race detector, `go vet`, Compose E2E, and Playwright visual QA all pass

## 19. Non-goals

- OpenAI Platform billing or API-key credit management
- purchasing credits or changing a ChatGPT subscription
- automatic reset without an available upstream reset credit
- browser fingerprint impersonation or anti-bot bypass
- arbitrary user-configured Codex OAuth or backend origins
- automatic publish after model discovery
- silently deleting published aliases when upstream discovery changes
- multi-admin authorization, teams, or remote public dashboard exposure
- guaranteed operation of undocumented quota endpoints after upstream contract changes

## 20. Source notes

The design is based on:

- official Codex authentication documentation: `https://developers.openai.com/codex/auth`
- current OpenAI Codex source behavior for OAuth, models, Responses, token refresh, plan claims, and rate-limit signals: `https://github.com/openai/codex`
- the open-source Sub2API implementation of ChatGPT Codex quota and reset-credit JSON operations: `https://github.com/Wei-Shaw/sub2api`

The `wham` quota and reset-credit contracts are observed implementation details, not a documented stable public OpenAI API. The fallback and capability rules in this design are mandatory.
