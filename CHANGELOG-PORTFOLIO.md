# CHANGELOG — portfolio grind 2026-07

## Audited baseline

- Repository: `github.com/AbubakarMahmood/go-rate-limiter`
- Base commit: `d69d1e50758fa08acc0856978dfdfc010b2a9244`
- Work branch: see `docs/VERIFICATION.md`
- Source archive supplied by the repository owner on 2026-07-26.

## Correctness changes

- Corrected weighted sliding-window retry timing so it can cross the current boundary and returns the first safe microsecond under the implemented floating-point model.
- Made token-bucket reset/retry timestamps use the store decision clock, including Redis `TIME`.
- Defined positive counts and all-or-nothing bulk behavior consistently.
- Added low-level store validation for clock resolution, exact Redis integer range, TTL mathematical horizons, finite token values, and overflow-relevant durations.
- Made peeks and denials read-only in both stores: no missing-key allocation, permit-state commit, or TTL refresh.
- Added per-key backward-clock clamps for committed memory state and equivalent committed Redis metadata.
- Strengthened deletion/context behavior and exact contention tests.
- Added deterministic memory/Redis visible-decision parity coverage.
- Made concurrency tests surface backend errors instead of silently losing goroutine failures.

## API and trust-boundary changes

- Replaced delimiter-concatenated subject keys with injective byte-length framing.
- Bounded request bodies and identity fields; rejected unknown fields, invalid UTF-8, controls, outer whitespace, null/fractional/non-positive counts, and trailing JSON values.
- Disabled caller-selected algorithms/tiers by default; retained an explicit trusted-boundary switch.
- Disabled reset unless an environment-only token is configured; required bearer authentication when enabled.
- Made dependency/timeout errors fail closed with generic `503` responses and structured internal logs.
- Rounded rate-limit reset/retry headers up so clients are not invited early.

## Configuration and lifecycle changes

- Raised the module floor from Go 1.24 to Go 1.25 and updated Gin, QUIC, and related transitive modules to remove every fixable package advisory reported on 2026-07-28.
- Loaded YAML into explicit defaults while preserving explicit invalid zero values.
- Rejected unknown YAML keys and multiple documents.
- Added complete timeout, Redis, metric-path, tier-name, count/window, and retention validation.
- Restricted Redis support to one standalone endpoint instead of implying Cluster support.
- Added server timeouts, structured request/recovery logs, graceful shutdown completion, and structured `net/http` errors.

## Observability and reproducibility changes

- Replaced caller-derived metric dimensions with bounded `algorithm`, configured `tier`, and finite `result` labels.
- Updated the Grafana dashboard to the new metric contract.
- Added required Redis CI, race/build/vet/format gates, pinned build images, image smoke, and complete Compose smoke.
- Added smoke coverage for API behavior, Prometheus scrape, Grafana dashboard provisioning, Redis failure, fail-closed responses, and recovery.
- Improved microbenchmarks by removing per-operation key formatting and explicitly scoped them to algorithm plus memory store.

## Claims reduced or removed

- Removed numerical benchmark tables from the README because the supplied numbers were not a durable, repeated, committed evidence artifact.
- Rejected “sub-millisecond p99,” “zero-allocation,” Gin-and-Fiber, Redis Cluster/Sentinel, and broad “production-ready” claims.
- Replaced “selectable per request” language with the actual default-deny policy boundary.
- Corrected stale status/reset paths and reset-auth documentation.

## Scope rejected

No fourth algorithm, gateway/auth platform, tenant/billing system, gRPC surface, policy database, Kubernetes/Helm layer, Redis topology expansion, multi-region design, or speculative optimization was added.

## Imported audit-runner verification

- `gofmt` and `git diff --check`: passed.
- YAML/JSON/shell static parsing: passed during the source pass; final rerun is recorded in `docs/VERIFICATION.md`.
- Extracted core production packages (`algorithms`, memory store, shared contracts) with a compatibility harness: `go test -race -count=1 ./...` passed under the exact pinned Go 1.26.5 toolchain.
- All production packages and test files passed isolated Go 1.26.5 build/test-compilation and `go vet` against minimal external API-shape stubs, catching internal interface/syntax/test mismatches without claiming real dependency compatibility or execution.
- Full repository Go/Redis/Docker execution was unavailable in that runner; the later promotion verification is recorded in `docs/VERIFICATION.md`.

## Promotion verification

On 2026-07-28, the canonical staged tree passed:

- real-module verification, formatting, vet, build, and every package test on Windows with Go 1.26.5; the config test binary was executed separately after Windows Application Control rejected only Go's temporary launch path;
- Linux build, vet, and the complete race-enabled suite with Go 1.26.5;
- required Redis integration tests against Redis 7.4.9; and
- image plus complete Compose smoke with Docker 29.5.2, including Prometheus scrape, Grafana provisioning, Redis loss, fail-closed responses, and recovery.
- pinned `govulncheck` v1.6.0 with no reachable symbol or imported-package vulnerabilities; one no-fix notice remains for the unused `x/crypto/openpgp` package.

## Close status

**Local gate complete; GitHub CI pending.** Do not add the close date or final DONE declaration until the published source commit's workflow is observed green.
