# Operations

## Deployment shapes

### Memory

Use for one process, local development, or tests. Every process owns independent limits. Running two memory-backed replicas doubles the apparent policy capacity and is therefore not a supported shared-limit topology.

### Redis

Use multiple service instances against exactly one standalone Redis endpoint. Each decision is one Lua script and uses Redis server time. This repository does not implement Redis Cluster/Sentinel clients, failover validation, cross-region replication semantics, or a high-availability claim.

## Failure policy

The service fails closed:

- store timeout/error: admission returns `503` and `Retry-After: 1`;
- store health failure: `/health` returns `503`;
- malformed or unsafe policy input: startup or request fails explicitly.

This protects the limited resource but couples availability to the store. Operators should alert on health failures and `result="error"`, and should not configure an orchestrator to route traffic to an unhealthy instance.

## Logging

Logs are JSON through `log/slog`. Startup records selected store/default policy and whether overrides/reset are enabled, without logging the reset token. Requests record method, route template, status, response bytes, and duration. Backend errors remain server-side.

## Metrics

Prometheus labels are `algorithm`, configured `tier`, and finite `result`. Never add identifier, resource, raw error, path, token, or other caller-controlled values as labels.

Useful queries include:

```promql
sum by (result) (rate(rate_limiter_decisions_total[5m]))
```

```promql
sum(rate(rate_limiter_decisions_total{result="denied"}[5m]))
/
sum(rate(rate_limiter_decisions_total{result=~"allowed|denied"}[5m]))
```

```promql
histogram_quantile(
  0.95,
  sum by (le) (rate(rate_limiter_decision_duration_seconds_bucket[5m]))
)
```

A histogram quantile from a local dashboard describes observed requests in that deployment; it is not a portable repository performance claim.

## Verification and recovery

```bash
make verify
REDIS_ADDR=127.0.0.1:6379 make test-redis
make smoke-compose
```

The Compose smoke deliberately stops Redis and proves both health and decisions fail closed, restarts Redis, then proves recovery. Run it after changes to scripts, Redis client behavior, health, Docker, Compose, Prometheus, or Grafana provisioning.

## Secrets and local defaults

The Compose default reset token and `admin/admin` Grafana credentials are for an isolated local demonstration only. Override them or remove externally exposed ports in any shared environment. Terminate TLS and enforce network/authentication controls outside this repository.
