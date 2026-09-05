# Provider Model Pools Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build provider-owned model routes with dynamic account pools, model metadata cards, permanent deletion, round-robin routing, and real-`429` quota failover.

**Architecture:** PostgreSQL remains the source of truth for discovery and route ownership. The control plane resolves a provider route into an ordered account pool while compiling a snapshot; the gateway receives that self-contained pool plus a per-model strategy and performs request-time routing without database access. Discovery metadata is normalized through one presentation module used by both discovery results and model cards.

**Tech Stack:** PostgreSQL migrations, Next.js/React/TypeScript, Zod, Node test runner, Go 1.22, Docker Compose, Playwright.

## Global Constraints

- A discovered source is identified by `(provider_id, upstream_model)` and must never merge across providers.
- Provider-backed membership is derived only from enabled accounts with `discovered_models.available = true` for that provider and model.
- `quota_failover` excludes an account only after a real upstream `429`; cached usage percentages never affect routing.
- `round_robin` ignores affinity and alternates healthy accounts per model.
- Missing provider metadata renders as unknown; model-name inference is forbidden.
- Deleting a public model retains discovery rows and removes the alias from virtual-key policies.
- Existing manual model routes and snapshots without per-model strategies retain current behavior.
- Mutations create drafts and audit events but never auto-publish.

## File Structure

- `control-plane/migrations/006_provider_model_pools.sql`: provider ownership and strategy columns/constraints/indexes.
- `control-plane/lib/model-metadata.ts`: normalize and merge bounded discovery metadata for presentation.
- `control-plane/lib/codex/operations.ts`: provider-specific grouping and provider-backed import.
- `control-plane/lib/model-routes.ts`: provider strategy updates and transactional hard deletion.
- `control-plane/lib/snapshots.ts`: resolve dynamic provider pools into declarative snapshots.
- `control-plane/lib/control.ts`: request validation for manual and provider-backed models.
- `control-plane/app/api/control/v1/[[...resource]]/route.ts`: richer model projection, update, and delete interfaces.
- `control-plane/app/api/control/v1/models/import-selection/route.ts`: provider-backed import contract.
- `control-plane/app/codex-client.ts`: discovery/provider model types and requests.
- `control-plane/app/components/model-discovery-modal.tsx`: import provider sources with a strategy.
- `control-plane/app/components/model-card.tsx`: provider, metadata, pool, strategy, and actions.
- `control-plane/app/dashboard-client.tsx`: model mutations and confirmation wiring.
- `control-plane/app/i18n.ts`, `control-plane/app/styles.css`: localized copy and card/switch styling.
- `internal/config/config.go`: optional per-model routing strategy.
- `internal/router/router.go`: round-robin cursor and per-model strategy selection.
- `internal/proxy/proxy.go`: quota cooldown derived from safe reset headers after `429`.
- Tests remain beside current integration and package tests.

---

### Task 1: Persist Provider Ownership and Per-Model Strategy

**Files:**
- Create: `control-plane/migrations/006_provider_model_pools.sql`
- Modify: `control-plane/lib/control.ts`
- Test: `control-plane/tests/integration.test.ts`
- Test: `control-plane/tests/validation.test.ts`

**Interfaces:**
- Produces model columns `provider_id: uuid | null` and `routing_strategy: 'manual' | 'round_robin' | 'quota_failover'`.
- Produces Zod schemas that distinguish manual explicit-account routes from provider-backed routes.

- [ ] **Step 1: Write failing migration and validation tests**

Add integration assertions that migrated legacy rows read as `{ provider_id: null, routing_strategy: 'manual' }`, invalid strategies violate the database constraint, and provider deletion sets ownership to null without deleting the disabled historical alias. Add literal Zod expectations for valid provider-backed input and invalid provider/strategy combinations.

```ts
assert.deepEqual(
  ModelSchema.parse({ alias: 'luna', upstream_model: 'gpt-5.6-luna', provider_id: providerId, routing_strategy: 'round_robin' }),
  { alias: 'luna', upstream_model: 'gpt-5.6-luna', provider_id: providerId, routing_strategy: 'round_robin', enabled: true },
);
```

- [ ] **Step 2: Run the tests and verify RED**

Run the integration and validation test files in the control-plane Docker test container. Expected failures: columns do not exist and `ModelSchema` still requires `accounts`.

- [ ] **Step 3: Add the additive migration and schemas**

The migration adds a nullable provider FK with `ON DELETE SET NULL`, adds the strategy with default `manual`, constrains its values, and adds `(provider_id, upstream_model)` lookup index. Keep `ModelSchema` as a discriminated union in behavior: provider-backed input requires a UUID and provider strategy; manual input requires a non-empty account list and defaults to `manual`.

- [ ] **Step 4: Run migration idempotency and validation tests**

Expected: the targeted tests pass twice with no schema drift.

- [ ] **Step 5: Commit**

```bash
git commit -m "feat: persist provider-backed model routes"
```

### Task 2: Normalize and Group Provider Model Metadata

**Files:**
- Create: `control-plane/lib/model-metadata.ts`
- Create: `control-plane/tests/model-metadata.test.ts`
- Modify: `control-plane/lib/codex/operations.ts`
- Modify: `control-plane/tests/codex-discovery.integration.test.ts`
- Modify: `control-plane/package.json`

**Interfaces:**
- Produces `normalizeModelMetadata(input): ModelMetadata`.
- Produces `mergeModelMetadata(items): ModelMetadata`.
- Changes `groupedDiscoveredModels` identity to `(provider_id, upstream_model)` and returns provider fields plus normalized metadata.

- [ ] **Step 1: Write failing pure normalization tests**

Use literal fixtures for Codex `{context_window:272000}`, API provider `{tool_call:true,function_call:true,blurb,tier,owned_by}`, nested capabilities, conflicting account values, and a completely unknown model. Assert that unknown stays `null`, maximum context is selected, explicit true wins, and first deterministic text wins.

```ts
assert.deepEqual(normalizeModelMetadata({ context_window: 272000, supported_in_api: true }), {
  context_window: 272000,
  tools: null,
  function_calling: null,
  reasoning: null,
  supported_in_api: true,
  tier: null,
  owner: null,
  description: null,
});
```

- [ ] **Step 2: Run the pure test and verify RED**

Expected: module import fails because `model-metadata.ts` does not exist.

- [ ] **Step 3: Implement the deep normalization module**

Keep field-alias knowledge private. Accept only finite positive numeric context sizes, explicit booleans, and bounded strings. Do not inspect the model name.

- [ ] **Step 4: Write failing provider-isolation integration test**

Seed two providers and accounts that both return `gpt-5.6-luna`; assert two grouped rows, each containing only its provider's accounts and metadata.

- [ ] **Step 5: Change grouping and run tests GREEN**

Join providers, group by provider identity and upstream model, use deterministic aggregate order, then normalize the safe stored metadata in TypeScript.

- [ ] **Step 6: Commit**

```bash
git commit -m "feat: expose provider model capabilities"
```

### Task 3: Import Provider Routes and Compile Dynamic Pools

**Files:**
- Modify: `control-plane/lib/codex/operations.ts`
- Modify: `control-plane/app/api/control/v1/models/import-selection/route.ts`
- Modify: `control-plane/lib/snapshots.ts`
- Modify: `control-plane/tests/codex-discovery.integration.test.ts`
- Modify: `control-plane/tests/snapshot.test.ts`
- Modify: `control-plane/tests/snapshot-envelope.integration.test.ts`

**Interfaces:**
- Changes `importSelection(client, { alias, provider_id, upstream_model, routing_strategy })`.
- Emits runtime models with `routing_strategy` and all eligible provider account names.
- Retains the existing manual mapping path.

- [ ] **Step 1: Write failing import contract tests**

Assert import rejects a missing provider, an unavailable model, and a strategy outside the two provider modes. Assert successful import stores provider/strategy, creates no mapping rows, audits safe fields, and creates exactly one draft.

- [ ] **Step 2: Run import tests and verify RED**

Expected: current function requires `account_ids` and inserts mappings.

- [ ] **Step 3: Implement provider-backed import**

Lock the provider, require at least one enabled matching discovery/account row, insert provider ownership and strategy, then audit and draft in the caller's transaction.

- [ ] **Step 4: Write failing snapshot pool tests**

Seed two available Codex accounts, one unavailable Codex account, and a same-model account under another provider. Assert the compiled model contains only the two available Codex names. Then add a newly discovered account and assert a fresh compile contains three names without editing the route.

- [ ] **Step 5: Implement snapshot pool resolution**

Load manual mappings and provider eligibility separately, ordered by account priority/name. For provider routes use discovery eligibility; for manual routes use mappings. Emit `routing_strategy`, and fail closed with `model <alias> has no eligible accounts`.

- [ ] **Step 6: Run snapshot and discovery integration tests GREEN**

Also verify v1 materialization remains unchanged and snapshot credentials remain memory-only.

- [ ] **Step 7: Commit**

```bash
git commit -m "feat: compile dynamic provider model pools"
```

### Task 4: Hard-Delete Public Models Safely

**Files:**
- Create: `control-plane/lib/model-routes.ts`
- Create: `control-plane/tests/model-routes.integration.test.ts`
- Modify: `control-plane/app/api/control/v1/[[...resource]]/route.ts`
- Modify: `control-plane/package.json`

**Interfaces:**
- Produces `deleteModelRoute(client, modelId): Promise<{ id: string; alias: string; deleted: true }>`.
- Produces `updateProviderModelStrategy(client, modelId, strategy)` for Task 6.

- [ ] **Step 1: Write failing deletion integration tests**

Seed a route referenced by two virtual keys and with mapping rows plus retained discovery evidence. Assert deletion removes the alias/mappings, removes only that alias from both key arrays, retains discovery rows, writes one safe audit event, creates one draft, and returns `404` for a missing UUID.

- [ ] **Step 2: Run the deletion test and verify RED**

Expected: current DELETE only sets `enabled=false`.

- [ ] **Step 3: Implement the transactional model-route module**

Lock the row, remove the alias with an `unnest`-based array rewrite preserving order, delete the route, audit `{ alias }`, then store a draft. Keep all behavior behind the one deletion interface.

- [ ] **Step 4: Route DELETE through the module and run GREEN**

Verify provider/account deletion tests still pass and no other resources are deleted.

- [ ] **Step 5: Commit**

```bash
git commit -m "feat: permanently delete public models"
```

### Task 5: Add Per-Model Round Robin and Real-429 Failover

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_v2_test.go`
- Modify: `internal/router/router.go`
- Modify: `internal/router/router_test.go`
- Modify: `internal/proxy/proxy.go`
- Modify: `internal/proxy/proxy_test.go`

**Interfaces:**
- Adds `config.Model.RoutingStrategy string` as `routing_strategy`.
- `Router.Plan` applies model strategy before global strategy and exposes a concurrency-safe per-alias cursor internally.
- `Proxy` cools a `429` account using a bounded reset duration derived from allowlisted response headers.

- [ ] **Step 1: Write failing config tests**

Assert `round_robin` and `quota_failover` are accepted, an invalid value is rejected, and a missing value inherits the global strategy at build time.

- [ ] **Step 2: Run config tests and verify RED**

Expected: Go model has no strategy field.

- [ ] **Step 3: Add and validate the config field**

Normalize missing strategy to the snapshot routing strategy. Accept existing global strategies plus the two new model strategies; reject unknown non-empty model strategies.

- [ ] **Step 4: Write failing router behavior tests**

Call the same round-robin alias four times with the same affinity and assert `primary, secondary, primary, secondary`. Assert unhealthy/saturated accounts are skipped. For quota failover assert repeated healthy plans remain primary-first and affinity cannot select a cooling primary.

- [ ] **Step 5: Implement router strategy selection**

Add a cursor map protected by the existing mutex. Rotate the filtered candidate slice for round robin. Skip affinity for both provider strategies. Preserve stable declared order for quota failover.

- [ ] **Step 6: Write failing proxy `429` reset tests**

Create a primary that returns `429` with `Retry-After`, then a successful secondary. Assert the same request succeeds through secondary and the next request starts on secondary. Add a response with Codex `x-*-primary-reset-at` and no `Retry-After`; assert it produces a positive bounded cooldown. Confirm cached quota state is never consulted.

- [ ] **Step 7: Implement safe cooldown selection and run Go tests GREEN**

Use `Retry-After`, then the earliest future allowlisted reset timestamp, then configured cooldown. Clamp unreasonable values to a safe maximum matching the longest supported Codex window. Never parse response bodies.

- [ ] **Step 8: Commit**

```bash
git commit -m "feat: route provider models across accounts"
```

### Task 6: Expose Provider Cards, Strategy Switch, and Delete UI

**Files:**
- Create: `control-plane/app/components/model-card.tsx`
- Create: `control-plane/app/model-card.ts`
- Create: `control-plane/tests/model-card.test.ts`
- Modify: `control-plane/app/api/control/v1/[[...resource]]/route.ts`
- Modify: `control-plane/app/codex-client.ts`
- Modify: `control-plane/app/components/model-discovery-modal.tsx`
- Modify: `control-plane/app/dashboard-client.tsx`
- Modify: `control-plane/app/i18n.ts`
- Modify: `control-plane/app/styles.css`
- Modify: `control-plane/tests/codex-ui.test.ts`
- Modify: `control-plane/package.json`

**Interfaces:**
- Produces pure `formatContextWindow`, `modelCapabilityItems`, and strategy-label helpers.
- Model GET projection returns provider metadata, normalized discovery summary, and dynamic eligible account IDs/count.
- Provider model PATCH accepts only `round_robin` or `quota_failover`.

- [ ] **Step 1: Write failing pure card tests**

Assert `272000` formats deterministically, unknown values return the localized no-data label, explicit false differs from unknown, and strategy labels map exactly in RU/EN.

- [ ] **Step 2: Run UI unit tests and verify RED**

Expected: helper module and translation keys do not exist.

- [ ] **Step 3: Implement the richer model projection**

Return provider fields, strategy, normalized metadata, and eligible account count/IDs. Keep manual projection compatible. Route strategy updates through `updateProviderModelStrategy` and create a draft/audit transactionally.

- [ ] **Step 4: Update discovery import UI**

Key selection state by `${provider_id}:${upstream_model}`, show provider and metadata, remove account-ID submission, and add a strategy select defaulting to `round_robin`.

- [ ] **Step 5: Build the model card module**

Render provider/adapter, description, context, explicit capability badges, account count, strategy control, edit/enable/delete actions, and no-data states. Keep mutation callbacks passed from the dashboard.

- [ ] **Step 6: Wire deletion confirmation and strategy mutation**

Extend the existing delete state to include `model`, call DELETE, refresh all dashboard data, and localize confirmation copy. PATCH strategy immediately creates a draft and reloads.

- [ ] **Step 7: Run unit tests and production build GREEN**

Run all control-plane tests and `npm run build` in Docker.

- [ ] **Step 8: Commit**

```bash
git commit -m "feat: manage provider model pools in panel"
```

### Task 7: End-to-End and Live Two-Account Verification

**Files:**
- Modify: `test/fakeupstream/main.go`
- Modify: `test/fakeupstream/codex_test.go`
- Modify: `test/e2e-codex.mjs`
- Modify: `control-plane/tests/codex-dashboard.spec.ts`
- Modify: `docs/codex-provider.md`

**Interfaces:**
- Adds deterministic fake Codex per-account route counters and controlled one-account `429` behavior.
- Documents the two model pool modes and publish requirement.

- [ ] **Step 1: Write failing fake-upstream and E2E assertions**

Seed two fake Codex accounts that both discover `gpt-5.6-luna`. Assert provider import compiles both, round robin exposes alternating gateway route headers, and quota failover retries from a controlled `429` primary to secondary.

- [ ] **Step 2: Run E2E and verify RED**

Expected: fake upstream lacks per-account behavior and UI/API lack the final interactions.

- [ ] **Step 3: Add deterministic fake behavior and complete E2E**

Key behavior by `ChatGPT-Account-ID`; do not log tokens. Add Playwright assertions for provider name, context, badges, strategy switch, and deletion confirmation.

- [ ] **Step 4: Run full automated verification**

Run `go test ./...`, full control-plane `npm test`, production Next build, and Codex E2E. Require zero failures.

- [ ] **Step 5: Verify the live panel and configured Codex accounts**

Fetch Codex discovery, confirm `gpt-5.6-luna` reports both configured Codex accounts under one provider source, import a uniquely named temporary alias, publish, and make non-destructive requests that confirm both account routes. Remove the temporary alias afterward and publish the cleanup draft. Do not force a real user's account to produce `429`; the real-`429` branch is proven through the controlled upstream.

- [ ] **Step 6: Rebuild and restart Docker services**

Rebuild control-plane and gateway, recreate them, verify `localhost:13000`, `/api/ready`, gateway health, and current images.

- [ ] **Step 7: Clean build cache and commit**

```bash
git commit -m "test: verify provider model pool workflows"
docker builder prune -af
```
