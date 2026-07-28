# RUBRIC — evidence-backed admission control

A score is useful only after every hard gate passes. A repository with an atomicity, trust-boundary, or evidence failure is not “mostly done.”

## Hard gates

- Exact admission under contention passes for memory and Redis.
- Denied bulk requests consume no state.
- Status/denials are observational.
- Redis uses Redis time and one atomic script per decision.
- Config and input fail loudly.
- Reset and policy selection have explicit trust boundaries.
- Full test/race/vet/build and container smoke evidence is green from a clean checkout.
- Public claims match executable evidence.

## Scored qualities (100)

| Area | Points | Full-credit standard |
|---|---:|---|
| Core correctness | 30 | All algorithms preserve the invariant, including boundaries, bulk permits, clocks, expiry, and retry timing. |
| Backend parity | 15 | Memory and Redis expose equivalent decisions for deterministic scenarios; differences are documented. |
| Concurrency evidence | 15 | Tests assert exact admitted permits under heavy contention and surface every goroutine/backend error. |
| Trust and failure boundaries | 15 | Policy overrides are safe, reset is authenticated/disabled, failures close, and errors do not leak. |
| Reproducibility | 10 | Clean commands pin relevant tools and visibly execute Redis, image, Compose, scrape, dashboard, and recovery gates. |
| Operability | 5 | Structured logs, real health, bounded metrics, timeouts, and graceful shutdown are intentional. |
| Explanatory quality | 5 | README and semantic docs explain why the design is correct and where it is not. |
| Scope discipline | 5 | No feature or performance theater distracts from the invariant. |

## Era status rule

- **DONE:** every hard gate green, at least 90/100, no unqualified material claim.
- **SOURCE-READY:** source and tests are implemented, but one or more clean-environment evidence gates have not run.
- **REOPENED:** an objective trigger in `REGRIND-TRIGGER.md` occurs.
- **FAILED:** atomicity/parity is false and cannot be repaired within the era ceiling.

Current status is recorded in `GRIND-CHECKLIST.md`, not inferred from prose or the CI badge.
