# Verification snapshot

## Source identity

- Audit date: 2026-07-26
- Local promotion date: 2026-07-28
- Repository: `github.com/AbubakarMahmood/go-rate-limiter`
- Base commit: `d69d1e50758fa08acc0856978dfdfc010b2a9244`
- Promotion branch: `codex/go-rate-limiter-closeout`
- Module minimum: Go 1.25.0
- Pinned CI/container toolchain: Go 1.26.5
- Pinned CI/Compose Redis: Redis 7.4.9 on Alpine 3.21
- Supported application OS: any Go-supported OS for direct builds; the committed container runtime is Alpine Linux. This is not a claim that every OS has been exercised.

## Imported audit evidence

The source audit used the exact Go 1.26.5 toolchain for formatting, isolated
core race tests, complete source/test compilation, and `go vet` against
dependency API-shape stubs. Its runner lacked real external modules, Redis,
and Docker, so those results were treated as source-preparation evidence rather
than a release receipt.

## Local promotion evidence

The exact modified index was materialized as a canonical temporary source tree
for Linux/container checks, avoiding unrelated working-tree state. Real module
versions from `go.sum` were used throughout.

Windows 11 with Go 1.26.5:

```text
go mod download                   -> pass
go mod verify                     -> all modules verified
gofmt -l .                        -> no files
go vet ./...                      -> pass
go build ./...                    -> pass
go test -count=1 ./...            -> all runnable package binaries passed;
                                      host Application Control blocked only
                                      the temporary config test launch
go test -c -o bin/config.test.exe \
  ./internal/config               -> pass
bin/config.test.exe -test.count=1 -> pass
make vuln                         -> no reachable symbol or package vulnerabilities
```

The Application Control event was host policy, not a test failure: the same
compiled config binary executed and passed from the repository path. The
canonical Linux run below executed the whole suite in one command.

Linux container with Go 1.26.5:

```text
go mod verify                     -> all modules verified
gofmt -l .                        -> no files
go vet ./...                      -> pass
go build ./...                    -> pass
go test -race -count=1 ./...      -> pass
```

Required Redis integration used a temporary isolated network and
`redis:7.4.9-alpine3.21`:

```text
REQUIRE_REDIS=1 go test -race -count=1 -v \
  ./internal/store ./internal/algorithms -run Redis
                                  -> pass
```

The canonical tree then ran the committed Compose gate with Docker 29.5.2:

```text
make smoke-compose
```

Observed result:

```text
smoke test passed: health, allow, deny, status, reset, and metrics
Compose smoke test passed: API, scrape, dashboard, and Redis fail-closed recovery
```

The script built the pinned Go/Alpine image, waited for Redis and the service,
confirmed Prometheus scraping and Grafana dashboard provisioning, stopped
Redis, observed fail-closed health/admission responses, restarted Redis,
confirmed recovery, and removed the temporary stack and volumes.

`govulncheck` v1.6.0 reported one module-level notice for
`golang.org/x/crypto/openpgp`, which is unmaintained and has no fixed release.
The project does not import or call that package; symbol and imported-package
results were both empty. Every advisory with an available fix in the selected
build graph was removed by the Go 1.25 dependency refresh.

## Remaining external gate

Publish the source commit and inspect its GitHub Actions workflow. Confirm:

- Redis tests log their target and do not skip;
- race/build/vet/format are green;
- image smoke is green; and
- full Compose smoke reports API, scrape, dashboard, Redis failure, and recovery green.

Only after that external receipt should `GRIND-CHECKLIST.md` be fully checked
and `CHANGELOG-PORTFOLIO.md` receive a close date and final DONE declaration.
