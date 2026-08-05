# 2papi Multi-account AI Gateway

A Go MVP for an OpenAI-compatible gateway with virtual-key auth, rate limits, sticky multi-account routing, cooldowns, circuits, and pre-stream failover.

## Features

- `/healthz`, `/readyz`, `/v1/models`, `/v1/chat/completions`.
- Generic OpenAI-compatible upstream proxy with public model alias rewriting.
- SSE and JSON response streaming without full response buffering.
- Multiple accounts per public model alias.
- Routing strategies: `priority`, `balanced`, `fastest`, `cheapest`, `quota-drain`, `fallback-chain`.
- Virtual API keys with constant-time keyed-HMAC comparison, model allowlists, and RPM token buckets.
- Sticky affinity from `X-Gateway-Session`, `metadata.gateway_session`, or stable user/model fallback.
- Account cooldowns, circuit breakers, concurrency caps, and route diagnostic headers.

## Docker-first development

The host does not need Go installed. Use the official Go Docker image:

```sh
docker run --rm -v "%cd%:/src" -w /src golang:1.22 go test -race ./...
docker run --rm -v "%cd%:/src" -w /src golang:1.22 go vet ./...
docker build -t 2papi-gateway .
```

Run the complete local stack with dashboard, PostgreSQL, Redis, gateway, and fake OpenAI-compatible upstreams:

```sh
docker compose up --build
```

Open the dashboard at `http://localhost:13000`. The OpenAI-compatible gateway remains at `http://localhost:18080`.

Call the gateway:

```sh
curl http://localhost:18080/healthz
curl http://localhost:18080/v1/models
curl -N http://localhost:18080/v1/chat/completions \
  -H "Authorization: Bearer sk-gateway-dev" \
  -H "Content-Type: application/json" \
  -H "X-Gateway-Session: demo" \
  -d "{\"model\":\"gpt-dev\",\"stream\":true,\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}]}"
```

Responses include `X-Gateway-Route` and `X-Gateway-Attempts`. Upstream authorization is replaced and never forwarded from the client.

## Configuration

Start from `config/example.yaml`. It defines a versioned immutable snapshot:

- `virtual_keys`: client keys, allowed models, and RPM limits.
- `models`: public aliases mapped to upstream model IDs and account lists.
- `accounts`: OpenAI-compatible base URLs and API keys.
- `routing`: strategy, sticky TTL, and max pre-commit attempts.
- `resilience`: cooldown and circuit-breaker thresholds.

The request hot path uses only an immutable in-memory snapshot. The dashboard stores desired state in PostgreSQL, publishes version notifications through Redis, and the Go gateway atomically adopts validated snapshots while retaining its last valid configuration if the control plane is unavailable.
