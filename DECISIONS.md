# DECISIONS — era 2026-07

These decisions are locked for this source era unless a regression or regrind trigger explicitly reopens them.

## Product and architecture

1. The repository remains a standalone Go admission-control HTTP service, not a gateway or hosted product.
2. Atomic admission is the primary invariant. Stores execute read/evaluate/write as one operation.
3. The era contains exactly token bucket, weighted sliding-window counter, and fixed-window counter.
4. `count` is positive and all-or-nothing. A denial consumes nothing; a count larger than policy capacity is invalid.
5. Status is observational. Peeks and denials do not allocate missing state, commit permit state, or refresh TTL.
6. Memory is supported for one service process. Redis is supported as one standalone endpoint shared by multiple service instances.
7. Redis decisions use `TIME` inside Lua. No application-clock synchronization is assumed.
8. Algorithm state namespaces are separate.

## Trust boundary

1. Algorithm/tier overrides are disabled by default and rejected when supplied. They may be enabled only for a trusted gateway or demonstration environment.
2. Reset is disabled when `RESET_TOKEN` is absent. When enabled, it requires a bearer token compared in constant time after hashing.
3. Reset secrets are environment-only; YAML cannot contain them.
4. Identifier/resource tuples use byte-length framing rather than delimiter concatenation.
5. Input is strict: bounded body, valid UTF-8, bounded identity components, no control/outer whitespace, no unknown JSON fields.

## Failure and operations

1. Dependency failures and decision timeouts fail closed with `503` and a short `Retry-After`.
2. Health calls the real store and is bounded by a timeout.
3. Error details go to structured logs, not client responses.
4. HTTP server read-header/read/write/idle timeouts are explicit, and shutdown is graceful.
5. Prometheus labels are restricted to algorithm, configured tier, and finite outcome values.

## Tooling and support

1. `go.mod` uses Go 1.25 as the minimum so the dependency graph can carry current security-fixed `x/*`, Gin, and QUIC releases.
2. CI and the container build pin Go 1.26.5 for the audited era.
3. CI and Compose pin standalone Redis 7.4.9 on Alpine 3.21.
4. Redis tests must fail, not skip, when CI declares `REQUIRE_REDIS=1`.
5. Docker image, direct API, complete Compose topology, Prometheus scrape, Grafana provisioning, Redis loss, and recovery have executable smoke paths.

## Claims and performance

1. No numerical latency, throughput, pXX, or allocation result is a durable claim in this era.
2. `make bench` measures algorithm plus in-memory store only; HTTP and Redis performance are separate experiments.
3. The dashboard is operational instrumentation, not correctness evidence.
4. “Production-ready,” Redis Cluster/Sentinel, zero-allocation, and sub-millisecond p99 language are rejected.

## Explicit cuts

Authentication/tenancy, billing, policy databases, a fourth algorithm, gRPC, Kubernetes/Helm, plugin frameworks, Redis topology expansion, multi-region design, and speculative optimization are outside this era.
