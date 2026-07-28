package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMetricsUseOnlyBoundedLabels(t *testing.T) {
	registry := prometheus.NewRegistry()
	m := New(registry)
	m.RecordDecision("token_bucket", "premium", ResultAllowed, 0.001)
	m.RecordDecision("token_bucket", "premium", ResultDenied, 0.002)
	m.RecordDecision("token_bucket", "premium", ResultInvalid, 0.0025)
	m.RecordDecision("fixed_window", "default", ResultError, 0.003)

	families, err := registry.Gather()
	require.NoError(t, err)

	seen := map[string]bool{}
	for _, family := range families {
		seen[family.GetName()] = true
		for _, metric := range family.Metric {
			labels := map[string]string{}
			for _, pair := range metric.Label {
				labels[pair.GetName()] = pair.GetValue()
			}
			assert.ElementsMatch(t, []string{"algorithm", "tier", "result"}, keys(labels))
			assert.NotContains(t, labels, "identifier")
			assert.NotContains(t, labels, "resource")
		}
	}
	assert.True(t, seen["rate_limiter_decisions_total"])
	assert.True(t, seen["rate_limiter_decision_duration_seconds"])
}

func keys(values map[string]string) []string {
	out := make([]string, 0, len(values))
	for key := range values {
		out = append(out, key)
	}
	return out
}
