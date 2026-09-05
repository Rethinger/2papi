# Design: High-performance multi-account AI API gateway

Date: 2026-08-04
Status: Draft for user review

## 1. Goal

Build a self-hosted AI API gateway that combines the multi-account switching experience of Sub2API, the low-overhead proxy path of LiteLLM, and quota/cost-aware routing inspired by 9Router and OmniRoute. Clients use one OpenAI-compatible endpoint while the gateway selects among many provider accounts and custom endpoints.

The supported credential boundary for the core product is official API keys, provider-supported OAuth, cloud credentials, and custom OpenAI-compatible endpoints. Cookie scraping, MITM interception, bypassing provider restrictions, and undocumented session-token extraction are outside the trusted core.

## 2. Product scope

### MVP

- OpenAI-compatible `/v1/chat/completions`, `/v1/responses`, `/v1/embeddings`, and `/v1/models`.
- SSE streaming without buffering complete responses.
- Provider adapters for OpenAI, Anthropic, Gemini, OpenRouter, Azure OpenAI, AWS Bedrock, and generic OpenAI-compatible endpoints.
- Multiple accounts per provider.
- Account health, quota estimates, cooldowns, weights, priorities, and manual enable/disable.
- Routing policies: priority, weighted round-robin, least-latency, least-cost, quota-drain, and ordered fallback.
- Sticky routing for conversational continuity and provider prompt-cache affinity.
- Virtual models and model aliases.
- Virtual API keys with model permissions, RPM/TPM limits, and budgets.
- Request logs, usage/cost accounting, health dashboards, and audit logs.
- Docker Compose deployment with Go gateway, Next.js control plane, PostgreSQL, and Redis.

### Later phases

- Image, audio, rerank, video, web search, MCP, and A2A endpoints.
- Semantic caching, prompt compression, guardrails, and evaluation framework.
- Organizations, teams, SSO, chargeback, Kubernetes/Helm, multi-region control plane.
- A separate community adapter SDK for experimental integrations. Experimental adapters cannot access the gateway secret store directly and must run out of process.

## 3. Architecture

### Go data plane

The gateway owns the latency-sensitive request path:

1. Authenticate the virtual key.
2. Normalize the incoming protocol into a canonical request representation.
3. Validate policy, model access, budget, and rate limits.
4. Obtain an immutable routing snapshot from local memory.
5. Select a deployment and account.
6. Translate and forward the request using pooled HTTP transports.
7. Stream the response while translating events incrementally.
8. Emit compact asynchronous usage and health events.

The request path must not query PostgreSQL. Redis is used only for distributed coordination that cannot be served from local snapshots, such as atomic rate counters, short-lived affinity, cooldowns, and distributed leases.

### TypeScript control plane

A Next.js application provides the dashboard and management API. It manages providers, accounts, models, policies, virtual keys, users, budgets, and audit records. Configuration changes create versioned snapshots that gateway instances receive through pub/sub and periodically reconcile.

### Storage

- PostgreSQL is the source of truth for durable configuration and aggregated usage.
- Redis holds rate-limit counters, short health state, affinity mappings, cooldowns, snapshot notifications, and optional cache metadata.
- Credentials are envelope-encrypted. A deployment master key or external KMS encrypts per-secret data keys. Plaintext credentials are only present in gateway memory while needed.

## 4. Module boundaries

- `protocol`: canonical request/response and streaming event types.
- `adapter`: provider-specific authentication, capability declaration, translation, error classification, and usage parsing.
- `catalog`: provider deployments, model capabilities, aliases, and pricing.
- `pool`: account eligibility, quota state, cooldowns, weights, and leases.
- `router`: candidate filtering, scoring, sticky affinity, and fallback plans.
- `resilience`: circuit breakers, retry budgets, timeouts, and health probes.
- `policy`: virtual-key permissions, budgets, rate limits, and routing rules.
- `proxy`: HTTP forwarding and incremental stream transformation.
- `telemetry`: non-blocking events, metrics, traces, cost and usage aggregation.
- `control-plane`: management API, dashboard, authentication, secret administration, and snapshot publishing.

Each adapter implements a stable interface and declares supported operations and model features. The router never contains provider-specific conditionals.

## 5. Routing model

Routing first filters candidates by model capability, policy, credential status, context size, required features, region, budget, and current circuit state. Eligible candidates receive a score based on:

- explicit tier and priority;
- remaining quota and time until reset;
- recent success rate and error class;
- exponentially weighted latency;
- estimated request cost;
- active load and concurrency;
- sticky-session or prompt-cache affinity.

Policies may compose these factors into predefined strategies:

- `priority`: ordered accounts and deployments;
- `balanced`: weighted power-of-two-choices balancing;
- `fastest`: latency-weighted selection;
- `cheapest`: estimated cost with a health floor;
- `quota-drain`: consume prepaid quota that resets soon;
- `fallback-chain`: explicit subscription, paid, cheap, and emergency tiers.

Randomness is applied within a narrow score band to avoid traffic herding.

## 6. Failure behavior

Circuit breakers exist independently for account, model deployment, and provider. A credential failure disables only the affected account. A model-specific failure locks only that deployment. Provider-wide transport failures affect the provider circuit.

- Retry and failover are allowed only before response bytes are committed to the client.
- Streaming requests are never silently replayed after output begins.
- 401/403 disables or quarantines the credential until revalidation.
- 429 applies a cooldown derived from provider headers, then exponential backoff with jitter.
- 5xx and transport failures affect rolling health and circuit state.
- Invalid requests and context-length errors are returned without retry unless a configured model fallback can satisfy the request.
- A per-request retry budget prevents cascading retries.

Optional hedged requests are restricted to idempotent, non-streaming operations and are disabled by default because they can duplicate provider cost.

## 7. Performance design

- Long-lived HTTP/2 or HTTP/1.1 connection pools per provider origin and outbound proxy.
- Zero full-response buffering for SSE.
- Immutable in-memory routing snapshots with atomic pointer swaps.
- Precompiled routing policies and model aliases.
- Asynchronous bounded telemetry queues with backpressure and sampling.
- No synchronous database writes in the request path.
- Incremental JSON/SSE transformation where provider protocols differ.
- Power-of-two-choices selection to avoid scanning large account pools on every request.

Initial performance objectives on ordinary server hardware:

- gateway overhead p50 below 5 ms and p99 below 20 ms, excluding provider latency;
- sustain at least 2,000 concurrent streaming connections per gateway instance;
- configuration propagation under 2 seconds;
- failover decision under 2 ms from the current snapshot.

These are engineering targets and must be validated by repeatable load tests.

## 8. Data model

Core entities:

- `providers`: adapter type and provider metadata.
- `accounts`: provider credential reference, status, labels, region, priority, and weight.
- `deployments`: account, upstream model, public alias, capabilities, pricing, and limits.
- `routing_policies`: strategy, candidate filters, tiers, weights, and fallback rules.
- `virtual_keys`: hashed key, owner, permissions, limits, budget, and policy.
- `quota_states`: observed limits, remaining estimate, reset time, and confidence.
- `health_states`: rolling latency, success rate, breaker state, and cooldown.
- `affinities`: conversation or cache key to selected deployment with TTL.
- `usage_events`: append-oriented request usage data.
- `usage_rollups`: hourly/daily aggregation by key, account, provider, and model.
- `audit_events`: immutable administrative actions.
- `config_versions`: versioned gateway snapshots.

Credentials are stored as encrypted secret records and are never returned by read APIs.

## 9. API behavior

The external API follows OpenAI conventions. Gateway extensions use optional headers and metadata:

- `X-Gateway-Policy`: select an allowed routing policy.
- `X-Gateway-Session`: explicit sticky-session key.
- `X-Gateway-Route`: returned route identifier without exposing secrets.
- `X-Gateway-Attempts`: number of pre-commit attempts.

The management API supports CRUD for providers, accounts, deployments, aliases, policies, and virtual keys, plus credential validation, model discovery, health inspection, usage reports, and key rotation.

## 10. Security

- Hash virtual API keys with a keyed hash and display them only once.
- Encrypt upstream credentials using envelope encryption.
- Redact authorization headers, request secrets, and configured sensitive fields from logs.
- Apply strict outbound destination allowlists to prevent SSRF from custom endpoints.
- Separate admin and gateway identities and database permissions.
- Record all credential, policy, key, and access-control changes in audit logs.
- Support optional prompt/body logging. Metadata-only logging is the default.
- Do not implement provider restriction bypasses in the trusted core.

## 11. Observability

Expose Prometheus metrics and OpenTelemetry traces for request counts, time to first token, total duration, gateway overhead, error classes, selected routes, retries, circuit state, rate-limit rejection, token usage, and estimated cost. High-cardinality identifiers are kept out of metric labels and remain in logs/traces.

## 12. Testing

- Unit tests for canonical translation, error classification, scoring, quota accounting, rate limits, and secret redaction.
- Contract fixtures for every provider adapter.
- Fake upstream servers for streaming fragmentation, malformed events, timeouts, 429s, partial output, and disconnects.
- Deterministic router simulations across large account pools.
- Integration tests with PostgreSQL and Redis.
- Load tests for concurrent SSE, snapshot swaps, and telemetry backpressure.
- Fault-injection tests proving account-, deployment-, and provider-level isolation.
- Security tests for SSRF, authorization boundaries, secret leakage, and malformed credentials.

## 13. Delivery decomposition

1. Foundation: repository, canonical protocol, generic OpenAI adapter, streaming proxy, configuration snapshots, and local development stack.
2. Multi-account routing: pool, health, cooldown, rate limits, sticky routing, and fallback.
3. Provider breadth: Anthropic, Gemini, OpenRouter, Azure OpenAI, and Bedrock adapters.
4. Control plane: dashboard, encrypted account management, model catalog, policies, and virtual keys.
5. Usage and operations: accounting, costs, audit, metrics, traces, load tests, and production hardening.

The first implementation plan should cover only phases 1 and 2. Provider breadth and the complete dashboard should receive separate implementation plans so the project remains reviewable and testable.

## 14. Explicit non-goals for the first plan

- Billing customers or processing payments.
- Multi-region consensus.
- Kubernetes operators.
- Cookie scraping, MITM interception, or undocumented authentication flows.
- Semantic cache, prompt rewriting, MCP, A2A, image, audio, and video services.
- Supporting hundreds of providers before the adapter SDK and contract suite are stable.
