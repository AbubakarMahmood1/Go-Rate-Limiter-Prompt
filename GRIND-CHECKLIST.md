# GRIND CHECKLIST — 2026-07

Status: **SOURCE AND CI EVIDENCE COMPLETE; PROFILE INTEGRATION PENDING**

`[x]` means implemented and observed against the modified source snapshot. `[ ]` means an external publication or CI receipt is still pending.

## Source truth

- [x] Base commit, branch, tool expectations, and environment limitations recorded.
- [x] Claims matrix committed.
- [x] Direct, image, Redis, and complete Compose commands committed.
- [x] Canonical staged tree built and tested on Windows and Linux with pinned Go 1.26.5.
- [x] Complete Compose smoke observed green with Docker 29.5.2.

## Correctness

- [x] Algorithm semantics and approximation limits documented.
- [x] All-or-nothing count and denial-without-consumption covered.
- [x] Status, reset, tier/default resolution, and algorithm separation covered.
- [x] Exact contention test exists for every algorithm on memory and Redis.
- [x] Invalid counts/windows, boundaries, expiry, encoding, collisions, clocks, and overflow-relevant validation covered.
- [x] Deterministic memory/Redis visible-decision parity test exists.

## Engineering and operations

- [x] Formatting check passes in the audit runner.
- [x] Core algorithm/memory extraction passes `go test -race` under the exact pinned Go 1.26.5 toolchain.
- [x] Complete production and test source compiles and passes `go vet` under Go 1.26.5 against isolated external API-shape stubs.
- [x] Redis tests become fatal instead of skipped when `REQUIRE_REDIS=1`.
- [x] Structured request/error logs, timeouts, graceful shutdown, real health, and fail-closed behavior implemented.
- [x] Metrics use bounded labels and dashboard queries match collector names.
- [x] Reset has an environment-only bearer-token boundary.
- [x] `go test ./...`, `go test -race ./...`, `go vet ./...`, and `go build ./...` observed green with real dependencies, the Go 1.25 module floor, and Go 1.26.5.
- [x] Redis integration suite observed green against Redis 7.4.9.
- [x] Image and Compose smoke observed green, including scrape/dashboard/failure/recovery.
- [x] Pinned `govulncheck` reports no reachable symbol or imported-package vulnerabilities.

## Claims and hiring surface

- [x] Unsupported p99, zero-allocation, framework, topology, and “production-ready” claims removed/rejected.
- [x] Benchmark scope and methodology documented without publishing stale numbers.
- [x] README opens with the invariant, includes architecture, trade-offs, failure policy, limits, and commands.
- [x] Truthful replacement profile copy recorded in `CLAIMS.md`.
- [ ] Public profile/portfolio/resume updated after this source branch is merged and evidence is green.
- [ ] Repository pinned only after the same evidence is green.
- [x] GitHub Actions observed green for published source commit `d14136e7d9208e65711832d5e642581e9f88df46`.

## Close-out

- [x] `SPIRIT.md`, `DECISIONS.md`, `RUBRIC.md`, `CLAIMS.md`, and `REGRIND-TRIGGER.md` exist.
- [x] `CHANGELOG-PORTFOLIO.md` records source changes and reduced claims.
- [x] Source publication and CI evidence items closed.
- [ ] Profile/resume adoption and repository-pin decision closed.
- [ ] Add `Closed on YYYY-MM-DD` and the final DONE declaration.

The repository must not be declared DONE while any unchecked item above remains.
