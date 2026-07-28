// Package metrics exposes the service's Prometheus instrumentation.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Decision outcomes. Keeping these values finite prevents accidental
// cardinality growth from error strings or caller-controlled input.
const (
	ResultAllowed = "allowed"
	ResultDenied  = "denied"
	ResultInvalid = "invalid"
	ResultError   = "error"
)

// Metrics holds the Prometheus collectors for admission decisions. Labels
// are restricted to finite, configuration-defined sets; caller-controlled
// identifiers and resources are deliberately never used as label values.
type Metrics struct {
	Decisions *prometheus.CounterVec
	Duration  *prometheus.HistogramVec
}

// New registers the collectors with reg and returns them. Tests pass their
// own registry; main passes prometheus.DefaultRegisterer.
func New(reg prometheus.Registerer) *Metrics {
	factory := promauto.With(reg)
	labels := []string{"algorithm", "tier", "result"}
	return &Metrics{
		Decisions: factory.NewCounterVec(
			prometheus.CounterOpts{
				Name: "rate_limiter_decisions_total",
				Help: "Admission decisions completed, by configured algorithm, tier, and outcome.",
			},
			labels,
		),
		Duration: factory.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "rate_limiter_decision_duration_seconds",
				Help:    "End-to-end latency of admission decisions, including backend errors.",
				Buckets: []float64{.0001, .0005, .001, .005, .01, .05, .1, .5, 1},
			},
			labels,
		),
	}
}

// RecordDecision records one completed admission attempt.
func (m *Metrics) RecordDecision(algorithm, tier, result string, seconds float64) {
	m.Decisions.WithLabelValues(algorithm, tier, result).Inc()
	m.Duration.WithLabelValues(algorithm, tier, result).Observe(seconds)
}
