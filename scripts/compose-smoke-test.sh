#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$ROOT_DIR"

COMPOSE=(docker compose -f docker/docker-compose.yml)
KEEP_STACK=${KEEP_STACK:-0}
TARGET_URL=${TARGET_URL:-http://127.0.0.1:8081}
RESET_TOKEN=${RESET_TOKEN:-local-development-reset-token}
PROMETHEUS_URL=${PROMETHEUS_URL:-http://127.0.0.1:9090}
GRAFANA_URL=${GRAFANA_URL:-http://127.0.0.1:3000}

cleanup() {
  local exit_code=$?
  if [[ $exit_code -ne 0 ]]; then
    "${COMPOSE[@]}" ps >&2 || true
    "${COMPOSE[@]}" logs --no-color >&2 || true
  fi
  if [[ "$KEEP_STACK" != "1" ]]; then
    "${COMPOSE[@]}" down --volumes --remove-orphans >/dev/null 2>&1 || true
  fi
  return "$exit_code"
}
trap cleanup EXIT

get_code() {
  local method=$1
  local url=$2
  local body=${3:-}
  local code

  if [[ -n "$body" ]]; then
    code=$(curl --silent --output /dev/null --write-out '%{http_code}' \
      --max-time 5 \
      --request "$method" --header 'Content-Type: application/json' \
      --data "$body" "$url" || true)
  else
    code=$(curl --silent --output /dev/null --write-out '%{http_code}' \
      --max-time 5 \
      --request "$method" "$url" || true)
  fi
  printf '%s' "${code:-000}"
}

wait_for_code() {
  local expected=$1
  local method=$2
  local url=$3
  local body=${4:-}
  local code=000

  for _ in $(seq 1 60); do
    code=$(get_code "$method" "$url" "$body")
    if [[ "$code" == "$expected" ]]; then
      return 0
    fi
    sleep 1
  done
  echo "expected HTTP $expected from $method $url, last response was $code" >&2
  return 1
}

"${COMPOSE[@]}" up --detach --build
TARGET_URL="$TARGET_URL" RESET_TOKEN="$RESET_TOKEN" ./scripts/smoke-test.sh

for endpoint in "$PROMETHEUS_URL/-/ready" "$GRAFANA_URL/api/health"; do
  ready=0
  for _ in $(seq 1 60); do
    if curl --fail --silent --show-error "$endpoint" >/dev/null 2>&1; then
      ready=1
      break
    fi
    sleep 1
  done
  if [[ $ready -ne 1 ]]; then
    echo "stack endpoint did not become ready: $endpoint" >&2
    exit 1
  fi
done

# Verify that Prometheus is scraping the service and Grafana provisioned the
# committed dashboard, not merely that their HTTP processes started.
prometheus_up=0
for _ in $(seq 1 60); do
  response=$(curl --fail --silent --show-error \
    "$PROMETHEUS_URL/api/v1/query?query=up%7Bjob%3D%22rate-limiter%22%7D" 2>/dev/null || true)
  if printf '%s' "$response" | grep -Eq '"value":\[[^]]*,"1"\]'; then
    prometheus_up=1
    break
  fi
  sleep 1
done
if [[ $prometheus_up -ne 1 ]]; then
  echo "Prometheus never reported the rate-limiter target as up" >&2
  exit 1
fi

dashboard_ready=0
for _ in $(seq 1 60); do
  response=$(curl --fail --silent --show-error --user admin:admin \
    "$GRAFANA_URL/api/dashboards/uid/rate-limiter" 2>/dev/null || true)
  if printf '%s' "$response" | grep -q '"uid":"rate-limiter"'; then
    dashboard_ready=1
    break
  fi
  sleep 1
done
if [[ $dashboard_ready -ne 1 ]]; then
  echo "Grafana did not provision the rate-limiter dashboard" >&2
  exit 1
fi

# Prove the declared fail-closed policy: when Redis is unavailable, health and
# admission decisions both return 503 rather than allowing traffic implicitly.
"${COMPOSE[@]}" stop redis >/dev/null
wait_for_code 503 GET "$TARGET_URL/health"
wait_for_code 503 POST "$TARGET_URL/v1/check" \
  '{"resource":"ci.redis.failure","identifier":"smoke"}'

"${COMPOSE[@]}" start redis >/dev/null
wait_for_code 200 GET "$TARGET_URL/health"
wait_for_code 200 POST "$TARGET_URL/v1/check" \
  '{"resource":"ci.redis.recovery","identifier":"smoke"}'

echo "Compose smoke test passed: API, scrape, dashboard, and Redis fail-closed recovery"
