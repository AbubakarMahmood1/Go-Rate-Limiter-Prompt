# CLAIMS MATRIX

Every material public claim should map to committed evidence or be removed.

| Claim | Scope/qualification | Evidence |
|---|---|---|
| Decisions are atomic under contention | One memory process; multiple app instances sharing one standalone Redis | Per-key memory mutex, one Redis Lua script, exact admission tests in `internal/algorithms` and `internal/store` |
| Bulk permits are all-or-nothing | Positive integer `count`; impossible counts are `400` | Algorithm tests, Redis integration denied-batch test, handler tests |
| Status is read-only | No permit-state write, missing-state allocation, or TTL refresh | Memory/Redis read-only tests and handler status test |
| Redis instances agree on decision time | Standalone Redis only | `TIME` inside both Lua scripts; token results return backend time |
| Memory and Redis expose matching decisions | Deterministic, no-boundary scenarios; absolute timestamps may differ | `TestRedisAlgorithms_MatchMemoryVisibleDecisions` |
| Retry timing is safe | Store clock at microsecond resolution; HTTP seconds rounded up | token/sliding retry tests and header-ceiling test |
| Failure is fail-closed | Backend error or timeout returns `503`, not allow | handler fake-backend tests and Compose Redis-loss smoke |
| Health represents decision dependency | Store ping, maximum one-second health timeout | handler health tests and Compose smoke |
| Metrics have bounded labels | Tier cardinality is configuration-controlled | collector labels and `TestMetricsUseOnlyBoundedLabels` |
| Config fails loudly | Explicit config file, strict keys, one document, validation | config loader/tests and startup wiring |
| Redis is horizontally shareable | Multiple service processes sharing the same standalone endpoint | Redis atomic scripts and contention test; no HA/topology promise |
| Three algorithms are implemented | Token bucket, weighted sliding counter, fixed counter | source and algorithm tests |
| Full stack is reproducible | Observed locally from the canonical staged tree; requires Docker/Compose | `make smoke-compose`, local 2026-07-28 receipt, CI compose job |
| Performance | No durable numerical claim | methodology only in `docs/BENCHMARKS.md` |

## Rejected public claims

- “sub-millisecond p99 latency”
- “zero-allocation optimizations”
- “Gin, Fiber” (the service uses Gin; Fiber is not part of this repository)
- Redis Cluster, Sentinel, automatic failover, or multi-region correctness
- unqualified “production-ready”

## Truthful profile/portfolio copy

> Go admission-control service with token-bucket, weighted sliding-window, and fixed-window policies; atomic memory and standalone-Redis decisions, all-or-nothing bulk permits, bounded Prometheus metrics, fail-closed dependency handling, and contention/parity tests.

After the clean verification gate passes, the profile can add:

> CI exercises the race detector, required Redis integration, the container image, Prometheus/Grafana provisioning, and Redis failure/recovery.

Do not add that second sentence before the modified workflow is observed green.
