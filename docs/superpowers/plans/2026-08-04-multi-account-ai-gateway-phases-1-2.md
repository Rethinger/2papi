# Implementation Plan: Multi-account AI Gateway, Phases 1-2

Date: 2026-08-04
Design: `docs/superpowers/specs/2026-08-04-multi-account-ai-gateway-design.md`

## Outcome

Deliver a runnable Go gateway MVP with an OpenAI-compatible streaming endpoint, generic OpenAI-compatible upstreams, multiple accounts, policy-based routing, sticky sessions, rate limiting, cooldowns, circuit breakers, and a Docker-based verification workflow.

## Constraints

- No database call in the request hot path.
- Configuration starts as a versioned YAML snapshot. PostgreSQL/Redis integration remains behind interfaces for the later control-plane phase.
- Only official API keys and custom OpenAI-compatible endpoints are supported in this implementation slice.
- Retry/failover is allowed only before client response bytes are committed.
- Native Go is unavailable on the host, so build/test commands run through the official Go Docker image.

## Repository layout

```text
cmd/gateway/                 process entrypoint
internal/config/             YAML loading and validation
internal/protocol/           OpenAI-compatible request metadata
internal/adapter/openai/     generic OpenAI-compatible upstream adapter
internal/router/             account pool, strategies, sticky affinity
internal/resilience/         cooldown and circuit breaker state
internal/policy/             virtual-key auth and token-bucket limits
internal/proxy/              request body handling, forwarding, streaming
internal/server/             HTTP routes and middleware
config/example.yaml          local runnable configuration
Dockerfile                   production image
compose.yaml                 gateway + fake upstream profile
```

## Task 1: Foundation

1. Initialize Go module and dependency policy.
2. Add config types for server, virtual keys, models, accounts, routing, limits, and resilience.
3. Add strict YAML validation and immutable snapshot construction.
4. Add `/healthz`, `/readyz`, and `/v1/models`.
5. Add Dockerfile and Compose configuration.
6. Verify configuration and process startup through Docker.

## Task 2: Generic OpenAI-compatible proxy

1. Accept `/v1/chat/completions` and preserve JSON request bodies.
2. Parse only routing-critical fields such as model, stream, user, and metadata.
3. Select an upstream deployment from the snapshot.
4. Rewrite public model aliases to upstream model IDs.
5. Forward standard headers while replacing authorization safely.
6. Stream SSE and ordinary JSON responses without full response buffering.
7. Return OpenAI-shaped gateway errors and route diagnostics headers.

## Task 3: Multi-account routing

1. Filter disabled, cooling, open-circuit, unsupported, and over-concurrency accounts.
2. Implement priority, balanced, fastest, cheapest, quota-drain, and fallback-chain strategies.
3. Use power-of-two choices for balanced pools.
4. Maintain EWMA latency and rolling success/failure state.
5. Apply account-level cooldown from 429 and Retry-After.
6. Open account circuits after configurable consecutive failures.
7. Build a bounded pre-commit fallback plan and prevent retry storms.

## Task 4: Policy and affinity

1. Authenticate virtual keys using constant-time comparison of keyed hashes derived at startup.
2. Enforce model allowlists.
3. Apply per-key RPM token buckets.
4. Derive affinity from `X-Gateway-Session`, explicit metadata, or a stable user/model fallback.
5. Keep TTL affinity in memory behind an interface suitable for Redis replacement.
6. Expose `X-Gateway-Route` and `X-Gateway-Attempts` without leaking account secrets.

## Task 5: Verification

1. Unit-test config validation, candidate filtering, strategies, cooldowns, circuits, auth, and rate limits.
2. Add fake upstream integration tests for JSON, fragmented SSE, 429 fallback, 500 fallback, partial streaming, and disconnects.
3. Run `go test -race ./...` in Docker.
4. Run `go vet ./...` in Docker.
5. Build the production Docker image.
6. Start a local fake upstream and gateway, then validate health, models, auth, streaming, sticky routing, and failover with HTTP requests.

## Acceptance criteria

- A client can call one `/v1/chat/completions` endpoint with a virtual key.
- Two or more accounts can serve one public model alias.
- The gateway switches accounts on pre-stream 429/5xx failures.
- Once streaming output begins, the request is not replayed.
- Sticky sessions prefer the same healthy account.
- Rate limits and model access policies reject requests before upstream dispatch.
- Tests pass with race detection and the production image builds.
- README documents a complete Docker-first local workflow.
