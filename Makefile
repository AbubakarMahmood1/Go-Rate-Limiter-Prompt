.PHONY: help build run test test-race test-redis test-coverage verify verify-redis vuln bench fmt fmt-check vet tidy clean docker-build docker-up docker-down docker-logs smoke smoke-compose load-test

BINARY_NAME=rate-limiter
DOCKER_COMPOSE=docker compose -f docker/docker-compose.yml

help: ## Show this help message
	@echo 'Usage: make [target]'
	@echo ''
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "  %-16s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

build: ## Build the server binary
	go build -trimpath -o bin/$(BINARY_NAME) ./cmd/server

run: ## Run the server
	go run ./cmd/server

test: ## Run all tests once (Redis tests skip unless REDIS_ADDR is set)
	go test -count=1 ./...

test-race: ## Run all tests with the race detector
	go test -race -count=1 ./...

test-redis: ## Require and run the Redis integration suite (set REDIS_ADDR)
	@test -n "$$REDIS_ADDR" || { echo 'REDIS_ADDR is required'; exit 1; }
	REQUIRE_REDIS=1 go test -race -count=1 -v ./internal/store ./internal/algorithms -run 'Redis'

test-coverage: ## Run tests and write an HTML coverage report
	go test -race -count=1 -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html
	@echo "coverage report: coverage.html"

verify: fmt-check vet build test-race ## Run the infrastructure-free verification gate

verify-redis: verify test-redis ## Run the full verification gate including Redis

vuln: ## Scan reachable Go code with the pinned vulnerability checker
	go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./...

bench: ## Run in-memory algorithm/store microbenchmarks
	go test -bench=. -benchmem -run='^$$' ./internal/algorithms/

fmt: ## Format Go code
	gofmt -w .

fmt-check: ## Fail when Go code is not formatted
	@unformatted="$$(gofmt -l .)"; \
	if [ -n "$$unformatted" ]; then \
		echo 'gofmt required for:'; echo "$$unformatted"; exit 1; \
	fi

vet: ## Run go vet
	go vet ./...

tidy: ## Tidy Go modules
	go mod tidy

clean: ## Remove build and test artifacts
	rm -rf bin/ load-test-results/
	rm -f coverage.out coverage.html cpu.prof mem.prof

docker-build: ## Build the Docker image
	docker build -f docker/Dockerfile -t rate-limiter:latest .

docker-up: ## Start the full stack (service, Redis, Prometheus, Grafana)
	$(DOCKER_COMPOSE) up -d --build
	@echo "rate limiter: http://localhost:8081"
	@echo "prometheus:   http://localhost:9090"
	@echo "grafana:      http://localhost:3000 (admin/admin)"

docker-down: ## Stop the stack
	$(DOCKER_COMPOSE) down

docker-logs: ## Tail stack logs
	$(DOCKER_COMPOSE) logs -f

smoke: ## Exercise health, allow, deny, status, reset, and metrics (set RESET_TOKEN)
	./scripts/smoke-test.sh

smoke-compose: ## Build and test the complete Compose stack, including Redis failure/recovery
	./scripts/compose-smoke-test.sh

load-test: ## Run a Vegeta load test against a running instance
	./scripts/load-test.sh

.DEFAULT_GOAL := help
