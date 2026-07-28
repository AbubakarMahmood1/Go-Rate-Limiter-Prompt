# Benchmark contract

## What the committed benchmark measures

```bash
make bench
```

This runs `go test -bench=. -benchmem` in `internal/algorithms` against the in-memory store. It measures:

- allowed admission over 100 precomputed keys;
- allowed admission on one contended hot key; and
- read-only peeks over 100 precomputed keys.

The benchmark includes algorithm and memory-store work. It deliberately avoids per-operation string formatting in the benchmark harness.

## What it does not measure

- HTTP parsing, routing, headers, JSON, or network latency;
- Redis scripting or a Redis round trip;
- TLS, proxies, containers, orchestration, or cross-host behavior;
- production p50/p95/p99;
- a representative application key distribution;
- service-level throughput under dependency failure.

## Reproducible publication procedure

Before publishing a number, record:

1. commit SHA and clean/dirty state;
2. exact `go version`;
3. operating system, kernel, CPU model, logical CPU count, and power mode;
4. command and environment variables;
5. at least ten samples, preferably with `-count=10`;
6. comparison via `benchstat` when comparing commits;
7. whether CPU frequency scaling, virtualization, or other workloads were present.

Example:

```bash
go test -run='^$' -bench='BenchmarkAllow' -benchmem -count=10 ./internal/algorithms > before.txt
# switch to the candidate commit in a clean worktree
go test -run='^$' -bench='BenchmarkAllow' -benchmem -count=10 ./internal/algorithms > after.txt
benchstat before.txt after.txt
```

Results belong in an evidence artifact with that context, not as timeless README facts.

## HTTP load experiments

`scripts/load-test.sh` uses Vegeta against a running service. Supply a unique identity/resource and keep policy overrides disabled unless testing a trusted boundary. Record store mode, Redis placement, service/container resources, connection reuse, duration, rate, and response-code distribution.

An HTTP load result is still not a production SLO or p99 claim without a deployment-representative environment and repeated evidence.
