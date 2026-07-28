# SPIRIT — go-rate-limiter

## Era

**Evidence-backed admission control — 2026-07**

## Problem

Services need a dependable answer to a deceptively small question: **may this identity perform this action now?** Under concurrency, a nearly correct answer is incorrect. A limiter that occasionally over-admits, consumes a denied bulk request, disagrees across instances because of clock skew, or reports a misleading retry time fails precisely when it is most needed.

## Hope

`go-rate-limiter` should feel boring in the best sense: authoritative, predictable, explainable, and measurable. A service owner should be able to choose a policy, run one or many limiter instances against the appropriate store, and trust that each decision preserves the same invariant. An operator should be able to see whether the service and store are healthy without mistaking dashboards for correctness.

## Users

- Backend engineers protecting an API or expensive operation.
- Platform engineers operating a shared limiter backed by one standalone Redis endpoint.
- Reviewers learning how concurrency, time, atomic state, and API semantics interact in a small distributed service.

## The soul

> One request produces one atomic, explainable admission decision. Correctness is identical under contention, and every public claim can be reproduced from a clean checkout.

The service is not valuable because it contains three algorithms, Redis, Prometheus, or Docker. Those are useful only when they serve that invariant.

## Desired feeling

- **Callers:** the answer is unambiguous and retry information is safe.
- **Operators:** configuration fails loudly, health reflects the dependency, and metrics illuminate behavior.
- **Maintainers:** algorithm logic is understandable, store semantics are explicit, and races are difficult to introduce.
- **Reviewers:** the repository proves hard claims instead of asking for trust.

## Non-goals for this era

- Building an API gateway, authentication service, billing system, or hosted SaaS.
- Adding algorithms or a policy language.
- Claiming Redis Cluster, Sentinel, global, failover, or multi-region guarantees.
- Adding Kubernetes solely for portfolio optics.
- Chasing latency or allocation numbers without a benchmark contract.
- Rewriting working components to satisfy a generic architecture template.

## Betrayals

- Over-admitting under contention.
- Partially consuming denied multi-permit requests.
- Using process-local time for a shared Redis decision.
- Presenting memory as horizontally consistent.
- Letting status or denial extend logical state.
- Returning a retry/reset value that invites a retry too early.
- Silently accepting malformed configuration.
- Exposing reset without an explicit trust boundary.
- Publishing p99, allocation, topology, or “production-ready” claims without reproducible evidence.
- Expanding features while the central invariant is weakly tested.
