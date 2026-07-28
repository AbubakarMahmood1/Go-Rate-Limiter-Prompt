# go-rate-limiter

[![CI](https://github.com/AbubakarMahmood/go-rate-limiter/actions/workflows/ci.yml/badge.svg)](https://github.com/AbubakarMahmood/go-rate-limiter/actions/workflows/ci.yml)
[![Go](https://img.shields.io/badge/Go-1.25%2B-00ADD8?logo=go)](https://go.dev/)
[![License](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

> One request produces one atomic, explainable admission decision. Under contention, the service admits no more permits than the configured policy allows.

`go-rate-limiter` is a small HTTP service for answering: **may this identity perform this action now?** It implements token-bucket, weighted sliding-window, and fixed-window policies over either an in-process store or one standalone Redis server.

The central contract is deliberately narrow:

- A multi-permit request is all-or-nothing; a denial consumes nothing.
- Memory and Redis execute each decision atomically.
- Redis-backed decisions use Redis `TIME`, not an application instance's clock.
- Status is observational: it neither consumes permits nor extends state lifetime.
- Dependency failures fail closed with `503`; they never become implicit allows.
- Public claims are tied to tests or committed verification commands.

## Architecture

```mermaid
flowchart LR
    C[Caller or trusted gateway] -->|POST /v1/check| H[HTTP boundary\nvalidate identity, policy, count]
    H --> A{Configured algorithm}
    A --> TB[Token bucket]
    A --> SW[Weighted sliding window]
    A --> FW[Fixed window]
    TB --> S{Atomic store operation}
    SW --> S
    FW --> S
    S --> M[Memory\nper-key mutex\nsingle process clock]
    S --> R[Standalone Redis\nLua script\nRedis TIME]
    A -->|allow / deny, remaining, safe retry| H
    H --> RESP[HTTP response\nrate-limit headers]
    H --> MET[Prometheus\nbounded labels only]
```

Atomicity belongs to the store. The algorithms contain no mutable shared state and cannot split a decision into an unsafe read followed by a write.

## Fastest complete demonstration

With Docker and Compose installed:

```bash
make smoke-compose
```

That command builds the image, starts the service with Redis, Prometheus, and Grafana, then proves:

1. health, allow, deny, status, authenticated reset, and metrics;
2. Prometheus is actually scraping the service;
3. Grafana provisioned the committed dashboard; and
4. Redis loss causes health and admission to return `503`, followed by recovery.

The stack is removed afterward unless `KEEP_STACK=1` is set.

For a direct in-memory run:

```bash
RESET_TOKEN=local-development-reset-token go run ./cmd/server
```

In another shell:

```bash
RESET_TOKEN=local-development-reset-token make smoke
```

The direct path loads `./config.yaml` when it exists, or validated built-in defaults otherwise. A path supplied with `CONFIG_FILE` must exist and parse successfully; startup never silently substitutes defaults for a bad explicit file.

## HTTP contract

| Method | Path | Meaning |
|---|---|---|
| `POST` | `/v1/check` | Decide atomically and consume permits only when allowed. |
| `GET` | `/v1/status?identifier=...&resource=...` | Read current state without consuming or refreshing it. |
| `POST` | `/v1/reset?identifier=...&resource=...` | Operational reset; disabled unless `RESET_TOKEN` is configured. |
| `GET` | `/health` | `200` only when the configured store can answer. |
| `GET` | `/metrics` | Prometheus exposition when metrics are enabled. |

### Check

```bash
curl -i -X POST http://localhost:8080/v1/check \
  -H 'Content-Type: application/json' \
  -d '{"resource":"api.orders.create","identifier":"user-123","count":3}'
```

A successful decision returns `200`:

```json
{
  "allowed": true,
  "limit": 120,
  "remaining": 117,
  "reset_at": "2026-07-26T12:34:56.789Z"
}
```

A normal policy denial returns `429`, includes a positive `Retry-After` header, and adds `retry_after` in whole seconds. The service also emits `X-RateLimit-Limit`, `X-RateLimit-Remaining`, and `X-RateLimit-Reset`; reset timestamps are rounded up rather than advertised early.

`count` defaults to one only when omitted. Zero, null, negative, fractional, or policy-impossible counts are `400` errors. Bodies are limited to 8 KiB, unknown JSON fields are rejected, and identifiers/resources must be valid bounded UTF-8 without outer whitespace or control characters.

### Policy boundary

Clients do **not** choose a more generous algorithm or tier by default. When `api.allow_policy_overrides` is false, supplying `algorithm` or `tier` is rejected.

`ALLOW_POLICY_OVERRIDES=true` is intended only behind a trusted gateway or for the local Compose demonstration. Named tiers remain configuration-defined and therefore finite.

### Status and reset

```bash
curl -s 'http://localhost:8080/v1/status?identifier=user-123&resource=api.orders.create'

curl -s -X POST \
  -H 'Authorization: Bearer local-development-reset-token' \
  'http://localhost:8080/v1/reset?identifier=user-123&resource=api.orders.create'
```

Reset is not a public client capability. It returns `404` when `RESET_TOKEN` is absent and requires a bearer token when enabled. The secret is environment-only and must be at least 16 bytes.

The internal subject key uses byte-length framing, so values such as `identifier=a:b, resource=c` cannot collide with `identifier=a, resource=b:c`.

## Algorithms

| Algorithm | Rule | Useful property | Explicit trade-off |
|---|---|---|---|
| `token_bucket` | Refill continuously at `requests/window`; hold at most `burst` permits. | Smooth average rate with controlled bursts. | Uses fractional token state. |
| `sliding_window` | `current + previous × (1 - elapsed/window)` | Smooths fixed-window boundaries with O(1) state. | A weighted approximation; it assumes prior-window requests were evenly distributed. |
| `fixed_window` | Count permits in aligned windows. | Smallest and simplest state. | Can admit nearly twice the limit across a boundary. |

For every algorithm, an allowed `count=n` consumes exactly `n`; a denied request consumes zero. Algorithms have separate key namespaces, so changing policy does not accidentally reuse another algorithm's state.

`Retry-After` is computed at microsecond store-clock resolution and rounded up for HTTP. Sliding-window retry math can cross into the next window when merely reaching the current boundary would still be too early.

The complete formulas, reset semantics, TTL rules, and boundary behavior are in [docs/SEMANTICS.md](docs/SEMANTICS.md).

## Backends and guarantees

| Backend | Supported shape | Clock | Atomic mechanism | What is not claimed |
|---|---|---|---|---|
| Memory | One service process | Process clock, clamped per committed key | Per-key mutex | Cross-process or horizontal consistency |
| Redis | Multiple service instances sharing one standalone Redis endpoint | Redis `TIME` | One Lua script per decision | Cluster, Sentinel, automatic failover, or multi-region guarantees |

State expiry is derived from algorithm mathematics, not used as an arbitrary cache timeout. Window data lives until it can no longer affect a decision. Token state lives through a complete refill, so expiry is observationally equivalent to a full bucket.

## Failure and observability policy

Backend errors and decision timeouts return `503 Service Unavailable` with `Retry-After: 1`. Error details are logged in structured JSON but are not reflected to callers. `/health` uses the real store and also returns `503` when decisions cannot be made.

Prometheus exposes:

- `rate_limiter_decisions_total{algorithm,tier,result}`
- `rate_limiter_decision_duration_seconds{algorithm,tier,result}`

`result` is one of `allowed`, `denied`, `invalid`, or `error`. Labels contain only implemented algorithms, configured tiers, and this finite result set. Caller-controlled identifiers and resources never become labels. Metrics cover requests that reached a resolved limiter decision; malformed requests rejected before policy resolution are not included.

The provisioned Grafana dashboard is an operational view, not proof of correctness.

## Verification

| Claim | Executable evidence |
|---|---|
| Exact admission under contention, memory | `TestConcurrentAdmissionIsExact` |
| Exact admission under contention, Redis | `TestRedisAlgorithms_ConcurrentAdmissionIsExact` |
| Denied bulk requests consume nothing | Algorithm and Redis integration tests |
| Status/denials do not mutate committed state | Memory, Redis, and handler regression tests |
| Memory/Redis visible-decision parity | `TestRedisAlgorithms_MatchMemoryVisibleDecisions` |
| Safe retry math across window boundaries | Sliding-window retry regression tests |
| Bounded metric labels | `TestMetricsUseOnlyBoundedLabels` |
| Fail-closed dependency behavior | Handler tests and `scripts/compose-smoke-test.sh` |
| Redis integration cannot silently skip in CI | `REQUIRE_REDIS=1` in `.github/workflows/ci.yml` |
| Reachable dependency advisories | `make vuln` with pinned `govulncheck` |

Run the infrastructure-free gate with Go 1.25 or newer:

```bash
make verify
```

Run the pinned reachable-code vulnerability scan:

```bash
make vuln
```

Run the Redis gate against one standalone Redis endpoint:

```bash
REDIS_ADDR=127.0.0.1:6379 make test-redis
```

Run the complete container gate:

```bash
make smoke-compose
```

CI pins the build toolchain and Redis image, runs formatting, vet, build, the race detector, required Redis integration tests, an image smoke test, and the full Compose failure/recovery test. See [docs/VERIFICATION.md](docs/VERIFICATION.md) for the audited snapshot and current evidence status.

## Performance claims

`make bench` runs Go microbenchmarks for the algorithm plus **in-memory** store only, using precomputed keys. It does not measure HTTP, Redis round trips, deployment p50/p95/p99, or production throughput.

No numerical performance result is treated as a durable repository claim. Record the command, commit, Go version, CPU, operating system, and repeated samples before publishing a comparison. See [docs/BENCHMARKS.md](docs/BENCHMARKS.md).

`scripts/load-test.sh` can drive a running HTTP service with Vegeta when that tool is installed. Its output is an environment-specific experiment, not a checked-in latency promise.

## Configuration

The committed [config.yaml](config.yaml) documents every key. Important environment overrides are:

| Variable | Purpose |
|---|---|
| `CONFIG_FILE` | Require a specific YAML file. |
| `PORT` | HTTP port. |
| `STORE` | `memory` or `redis`. |
| `REDIS_ADDR` | Exactly one standalone Redis address. |
| `REDIS_PASSWORD` | Redis password. |
| `ALLOW_POLICY_OVERRIDES` | Permit trusted callers to select algorithm/tier. |
| `RESET_TOKEN` | Enable authenticated operational reset. |

Unknown YAML keys, multiple YAML documents, non-positive timeouts, invalid windows/counts, unsafe retention combinations, invalid metric paths, and unsupported Redis address shapes fail startup.

## Known limits

- Redis support is deliberately standalone only.
- The memory backend is deliberately single-process only.
- Sliding-window counting is a weighted approximation, not an exact event log.
- There is no tenant/authentication system, policy database, billing layer, or public reset authorization model.
- The service fails closed; availability therefore depends on the selected store.
- The HTTP API is JSON over HTTP/1.1 or HTTP/2 as provided by the Go server; no gRPC contract is claimed.

Operational guidance is in [docs/OPERATIONS.md](docs/OPERATIONS.md), and security boundaries are in [SECURITY.md](SECURITY.md).

## Project layout

```text
cmd/server/          service wiring, HTTP lifecycle, structured logs
internal/algorithms/ token bucket, weighted sliding window, fixed window
internal/store/      atomic memory and standalone Redis implementations
internal/handlers/   HTTP validation, policy boundary, headers, reset
internal/config/     strict YAML/env loading and validation
internal/metrics/    bounded Prometheus collectors
pkg/limiter/         shared contracts and result types
docker/              image, Compose, Prometheus, Grafana provisioning
scripts/             direct, Compose, and load-test verification
docs/                semantics, operations, benchmarks, evidence
```

## License

[MIT](LICENSE)
