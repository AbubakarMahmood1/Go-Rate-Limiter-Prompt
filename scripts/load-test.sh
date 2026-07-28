#!/bin/sh

# Load testing script using Vegeta.
# Usage: ./scripts/load-test.sh [duration] [rate]
# Set LOAD_TEST_POLICY_OVERRIDES=true only when the service was intentionally
# started with ALLOW_POLICY_OVERRIDES=true.

set -eu

DURATION=${1:-30s}
RATE=${2:-1000}
TARGET_URL=${TARGET_URL:-http://127.0.0.1:8080}
OUTPUT_DIR=${OUTPUT_DIR:-./load-test-results}
LOAD_TEST_POLICY_OVERRIDES=${LOAD_TEST_POLICY_OVERRIDES:-false}
RUN_ID=${RUN_ID:-$(date +%s)-$$}

printf '%s\n' \
    '===================================' \
    'Rate Limiter Load Test' \
    '===================================' \
    "Target URL: $TARGET_URL" \
    "Duration: $DURATION" \
    "Rate: $RATE req/sec" \
    "Policy overrides: $LOAD_TEST_POLICY_OVERRIDES" \
    '==================================='

mkdir -p "$OUTPUT_DIR"

if ! command -v vegeta >/dev/null 2>&1; then
    echo "Error: vegeta is not installed" >&2
    echo "Install with: go install github.com/tsenart/vegeta@latest" >&2
    exit 1
fi

write_target() {
    resource=$1
    identifier=$2
    algorithm=$3

    printf 'POST %s/v1/check\nContent-Type: application/json\n\n' "$TARGET_URL"
    if [ "$LOAD_TEST_POLICY_OVERRIDES" = "true" ]; then
        printf '{"resource":"%s","identifier":"%s","algorithm":"%s"}\n\n' \
            "$resource" "$identifier" "$algorithm"
    else
        printf '{"resource":"%s","identifier":"%s"}\n\n' \
            "$resource" "$identifier"
    fi
}

{
    write_target api.users.create "user-$RUN_ID-1" token_bucket
    write_target api.posts.create "user-$RUN_ID-2" sliding_window
    write_target api.comments.create "user-$RUN_ID-3" fixed_window
} > "$OUTPUT_DIR/targets.txt"

echo "Running load test..."
vegeta attack \
    -targets="$OUTPUT_DIR/targets.txt" \
    -duration="$DURATION" \
    -rate="$RATE" \
    -workers=10 \
    > "$OUTPUT_DIR/results.bin"

echo "Load test complete. Generating reports..."
vegeta report "$OUTPUT_DIR/results.bin" > "$OUTPUT_DIR/report.txt"
vegeta plot "$OUTPUT_DIR/results.bin" > "$OUTPUT_DIR/plot.html"
vegeta report -type='hist[0,1ms,5ms,10ms,50ms,100ms,500ms,1s,5s]' \
    "$OUTPUT_DIR/results.bin" > "$OUTPUT_DIR/histogram.txt"

printf '\n%s\n' '===================================' 'Load Test Results' '==================================='
cat "$OUTPUT_DIR/report.txt"
printf '\n%s\n' '===================================' 'Latency Histogram' '==================================='
cat "$OUTPUT_DIR/histogram.txt"
printf '\nResults saved to: %s\n' "$OUTPUT_DIR"
