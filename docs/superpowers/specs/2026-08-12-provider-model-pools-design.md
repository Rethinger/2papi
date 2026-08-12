# Provider Model Pools Design

## Goal

Make imported models provider-owned routes instead of one-account routes. The panel must show which provider supplies each model, expose discovered model capabilities, allow permanent model deletion, and let an operator choose between round-robin use of compatible accounts and failover after a real upstream quota response.

## Scope

This design covers:

- provider-specific discovery results;
- importing a discovered model as a provider-backed public route;
- dynamic membership of compatible accounts from that provider;
- per-model `round_robin` and `quota_failover` routing;
- model capability and context metadata in the model card;
- permanent deletion of an imported public model;
- compatibility for existing manually assigned model routes.

It does not infer capability data the provider did not return, predict quota exhaustion from cached usage percentages, delete discovery history when a public route is deleted, or automatically publish a draft.

## Domain Model

### Discovered model identity

A discovered model is identified by `(provider_id, upstream_model)`. The same upstream name returned by two providers represents two distinct sources. Account-level discovery rows remain the evidence used to determine which accounts currently offer that source.

The grouped discovery interface returns:

- `provider_id`, provider slug, provider name, and adapter;
- `upstream_model` and display name;
- normalized capability summary;
- safe raw metadata summary;
- availability per account belonging to that provider;
- count of currently available compatible accounts.

The discovery UI must never merge same-named models across providers.

### Public model route

`model_aliases` gains nullable provider ownership and a per-model routing strategy:

- `provider_id uuid NULL REFERENCES providers(id)`;
- `routing_strategy text NOT NULL DEFAULT 'manual'`, constrained to `manual`, `round_robin`, or `quota_failover`.

Provider-backed routes have a non-null `provider_id` and use either `round_robin` or `quota_failover`. Existing and manually created routes retain `provider_id = NULL` and `routing_strategy = 'manual'`.

The public model card derives its provider and metadata through the route's provider and matching discovery rows. Discovery metadata is not copied into the route, avoiding stale duplicate state.

## Dynamic Account Pool

For a provider-backed route, an account is eligible when all conditions hold:

1. the account belongs to the route's provider;
2. the provider and account are enabled;
3. an account-level discovery row for the route's `upstream_model` exists and has `available = true`;
4. the account is present in the compiled runtime snapshot.

The snapshot compiler resolves this pool at compile time and emits the ordered account names on the model. No request-time database dependency is introduced into the gateway.

When discovery later marks an account unavailable, or a provider/account is disabled or deleted, the next draft excludes it. When a new account discovers the same model, the next draft includes it automatically. Publishing remains an explicit operator action.

A provider-backed model with no eligible account makes the draft invalid rather than silently falling back to an unrelated provider.

Manual routes continue to use `model_account_mappings` exactly as they do today. Provider-backed routes do not require mapping rows; mappings that predate migration are ignored once provider ownership is set.

## Per-Model Routing

The runtime model interface adds `routing_strategy`. Legacy snapshots and models without this field inherit the existing global routing strategy.

### Round robin

`round_robin` chooses a different healthy eligible account for each new request in cyclic order. The cursor is held in gateway memory per public model alias and is concurrency-safe. The router skips disabled, cooling, circuit-open, or saturated accounts.

Session affinity must not override round robin. This ensures successive independent or same-session requests actually alternate as the operator requested.

If an attempted account returns `429` or a retryable `5xx` before the downstream response is committed, the same request proceeds to the next eligible account up to the configured maximum attempts.

### Quota failover

`quota_failover` preserves the provider pool's stable order. The first healthy account is primary. It remains primary across successful requests.

Only an actual upstream `429` marks an account quota-limited. Cached usage percentages never exclude an account. The current request immediately retries on the next eligible account when possible.

The exclusion duration is selected in this order:

1. a valid positive `Retry-After` header;
2. a valid Codex primary or secondary reset timestamp observed in rate-limit response headers;
3. the configured resilience cooldown.

After that duration the account becomes eligible again. Retryable `5xx` responses continue to use circuit-breaker behavior and do not permanently advance the quota primary.

Session affinity must not force routing back to a quota-limited account. For all other conditions, quota failover preserves a stable primary-first order.

## Discovery Metadata Normalization

Discovery persists the provider response after recursive secret filtering and size limiting. A normalized presentation module derives, when present:

- context window/token limit;
- tool use and function calling support;
- reasoning support;
- API availability;
- provider tier;
- model owner;
- description/blurb.

It accepts common equivalent field names such as `context_window`, `context_length`, or nested capability fields. Boolean values are displayed only when explicitly supplied or unambiguously represented by the provider. Missing values render as “No data”; the system must not guess from a model name.

When account rows disagree, the grouped result uses the maximum numeric context window, true if any available row explicitly supports a capability, false only if every available row explicitly rejects it, and unknown otherwise. Description, tier, and owner use the first non-empty value in deterministic account order.

## Control-Plane Interfaces

### Import

The import request becomes:

```json
{
  "alias": "gpt-5.6-luna",
  "provider_id": "provider-uuid",
  "upstream_model": "gpt-5.6-luna",
  "routing_strategy": "round_robin"
}
```

The control plane verifies that the provider exists and at least one enabled account currently has an available matching discovery row. Import creates a draft but does not publish it.

### Update

A provider-backed model can switch only between `round_robin` and `quota_failover`. Manual routes remain `manual` and continue to edit explicit account mappings. Converting between manual and provider-backed ownership is outside this scope; deletion and re-import is the clear path.

### Delete

`DELETE /api/control/v1/models/:id` performs a hard transactional deletion:

1. lock and load the model alias;
2. remove that alias from every `virtual_keys.models` array;
3. delete the model alias, cascading mapping rows;
4. write an audit event containing only safe identifiers;
5. store a new draft.

Discovery rows remain intact, so the source can be imported again. A missing model returns `404`. The UI requires confirmation and reloads cards and key policy summaries after success.

## Panel Design

Each model card shows:

- public alias and upstream model;
- provider name and adapter, or “Manual pool” for legacy routes;
- active compatible account count;
- strategy switch with “Alternate accounts” and “Switch after 429” for provider-backed routes;
- context window;
- capability badges for tools, function calling, reasoning, and API availability;
- optional owner, tier, and description;
- explicit “No data” for absent capability/context metadata;
- Edit, Enable/Disable, and Delete actions.

The discovery modal groups rows by provider and upstream model. Importing selects the provider source, not individual accounts. The default strategy is `round_robin`; the operator can change it before import.

## Migration and Compatibility

The migration is additive and idempotent. Existing model rows become `manual`; their mapping rows and runtime behavior remain unchanged. Existing schema-v1 and schema-v2 snapshots without model strategies remain accepted and inherit the global strategy.

The new snapshot shape remains schema version 2 because all fields are additive and optional to older declarative rows. The gateway config parser defaults a missing model strategy to the snapshot's global routing strategy.

## Errors and Safety

- Cross-provider account selection is impossible for provider-backed models.
- An invalid strategy is rejected at validation and config-build seams.
- A route with no eligible provider account prevents draft compilation.
- `429` response bodies and credentials are never persisted or logged.
- Only bounded, allowlisted rate-limit headers influence cooldown timing.
- Model deletion never deletes accounts, providers, credentials, or discovery evidence.
- All mutations are audited and create drafts transactionally.

## Verification

Automated verification must cover:

- migration defaults existing models to `manual`;
- same-named discoveries from two providers remain separate;
- metadata normalization and unknown-value behavior;
- provider import includes every currently available account from only that provider;
- newly discovered compatible accounts enter the next snapshot automatically;
- unavailable/disabled/deleted accounts leave the next snapshot;
- round robin alternates two Codex accounts and skips unhealthy accounts;
- quota failover stays on the primary until a real `429`, retries the same request on secondary, and respects reset timing;
- legacy models retain global-strategy behavior;
- deletion removes the route, mappings, and virtual-key references while retaining discovery rows;
- API and UI validation for both strategies;
- production Docker build and full Go/control-plane test suites.

Live verification uses the two configured Codex accounts. Discovery must show `gpt-5.6-luna` under the Codex provider with both accounts, the compiled route must contain both account names, round-robin requests must expose alternating `X-Gateway-Route` values, and a controlled `429` test must route the retry to the second account without relying on cached quota percentages.
