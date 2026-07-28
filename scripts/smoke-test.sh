#!/bin/sh

set -eu

TARGET_URL=${TARGET_URL:-http://127.0.0.1:8080}
RESET_TOKEN=${RESET_TOKEN:-}
SMOKE_COUNT=${SMOKE_COUNT:-120}
SMOKE_ID=${SMOKE_ID:-smoke-$(date +%s)-$$}
RESOURCE=${SMOKE_RESOURCE:-api.smoke}

if [ -z "$RESET_TOKEN" ]; then
    echo "RESET_TOKEN is required so the operational reset path is actually tested" >&2
    exit 1
fi

workdir=$(mktemp -d)
trap 'rm -rf "$workdir"' EXIT HUP INT TERM

request() {
    method=$1
    path=$2
    body=${3:-}
    output_prefix=$4
    shift 4

    if [ -n "$body" ]; then
        curl -sS -X "$method" "$TARGET_URL$path" \
            -H 'Content-Type: application/json' \
            "$@" \
            -D "$workdir/$output_prefix.headers" \
            -o "$workdir/$output_prefix.body" \
            -w '%{http_code}' \
            --data "$body"
    else
        curl -sS -X "$method" "$TARGET_URL$path" \
            "$@" \
            -D "$workdir/$output_prefix.headers" \
            -o "$workdir/$output_prefix.body" \
            -w '%{http_code}'
    fi
}

expect_code() {
    actual=$1
    expected=$2
    name=$3
    if [ "$actual" != "$expected" ]; then
        echo "$name: expected HTTP $expected, got $actual" >&2
        cat "$workdir/$name.body" >&2 || true
        exit 1
    fi
}

curl -fsS "$TARGET_URL/health" > "$workdir/health.body"
grep -q '"status":"ok"' "$workdir/health.body"

body=$(printf '{"resource":"%s","identifier":"%s","count":%s}' "$RESOURCE" "$SMOKE_ID" "$SMOKE_COUNT")
code=$(request POST /v1/check "$body" allow)
expect_code "$code" 200 allow
grep -q '"allowed":true' "$workdir/allow.body"

single=$(printf '{"resource":"%s","identifier":"%s"}' "$RESOURCE" "$SMOKE_ID")
code=$(request POST /v1/check "$single" deny)
expect_code "$code" 429 deny
grep -q '"allowed":false' "$workdir/deny.body"
grep -qi '^Retry-After: [1-9][0-9]*' "$workdir/deny.headers"

query="identifier=$SMOKE_ID&resource=$RESOURCE"
code=$(request GET "/v1/status?$query" '' status)
expect_code "$code" 200 status
grep -q '"remaining":0' "$workdir/status.body"

code=$(request POST "/v1/reset?$query" '' reset -H "Authorization: Bearer $RESET_TOKEN")
expect_code "$code" 200 reset
grep -q '"status":"reset"' "$workdir/reset.body"

code=$(request POST /v1/check "$single" after-reset)
expect_code "$code" 200 after-reset
grep -q '"allowed":true' "$workdir/after-reset.body"

curl -fsS "$TARGET_URL/metrics" > "$workdir/metrics.body"
grep -q '^rate_limiter_decisions_total' "$workdir/metrics.body"

echo "smoke test passed: health, allow, deny, status, reset, and metrics"
