// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package cost

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

// ValidationOutcome classifies the operational outcome of a webhook cost validation.
type ValidationOutcome string

const (
	OutcomeWithinBudget  ValidationOutcome = "within_budget"
	OutcomeOverBudget    ValidationOutcome = "over_budget"
	OutcomeUnknownBounds ValidationOutcome = "unknown_bounds"
	OutcomeRatcheted     ValidationOutcome = "ratcheted"
	OutcomeTimeout       ValidationOutcome = "timeout"
	OutcomeSaturated     ValidationOutcome = "saturated"
	OutcomeError         ValidationOutcome = "error"
)

var (
	// EvaluationsTotal counts the total number of CEL cost evaluations by resource kind and verdict.
	EvaluationsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "openchoreo_cel_cost_evaluations_total",
			Help: "Total number of static CEL cost evaluations partitioned by resource kind and verdict.",
		},
		[]string{"kind", "verdict"},
	)

	// AdmissionRejectionsTotal counts admission rejections caused by CEL cost violations.
	AdmissionRejectionsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "openchoreo_cel_cost_admission_rejections_total",
			Help: "Total number of admission rejections due to CEL cost budget breaches.",
		},
		[]string{"kind"},
	)

	// AdmissionWarningsTotal counts admission warnings emitted for CEL cost violations.
	AdmissionWarningsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "openchoreo_cel_cost_admission_warnings_total",
			Help: "Total number of admission warnings emitted for CEL cost violations.",
		},
		[]string{"kind"},
	)

	// ValidationTotal counts webhook cost validations by kind, mode, and operational outcome
	// (including timeout and semaphore saturation) to support data-driven warn -> enforce rollouts.
	ValidationTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "openchoreo_cel_cost_validation_total",
			Help: "Total number of admission CEL cost validations by resource kind, mode, and outcome.",
		},
		[]string{"kind", "mode", "outcome"},
	)

	// ValidationDurationSeconds observes static CEL cost validation latency by resource kind.
	ValidationDurationSeconds = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "openchoreo_cel_cost_validation_duration_seconds",
			Help:    "Duration of admission CEL cost validations in seconds.",
			Buckets: []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1.0, 2.0},
		},
		[]string{"kind"},
	)
)

func init() {
	metrics.Registry.MustRegister(
		EvaluationsTotal,
		AdmissionRejectionsTotal,
		AdmissionWarningsTotal,
		ValidationTotal,
		ValidationDurationSeconds,
	)
}

// RecordEvaluation increments metrics for a completed CEL cost evaluation.
func RecordEvaluation(kind string, verdict Verdict) {
	EvaluationsTotal.WithLabelValues(kind, string(verdict)).Inc()
}

// RecordRejection increments the rejection counter for a given kind.
func RecordRejection(kind string) {
	AdmissionRejectionsTotal.WithLabelValues(kind).Inc()
}

// RecordWarning increments the warning counter for a given kind.
func RecordWarning(kind string) {
	AdmissionWarningsTotal.WithLabelValues(kind).Inc()
}

// RecordValidation records the operational outcome and latency of a webhook cost validation.
func RecordValidation(kind string, mode Mode, outcome ValidationOutcome, duration time.Duration) {
	ValidationTotal.WithLabelValues(kind, string(mode), string(outcome)).Inc()
	ValidationDurationSeconds.WithLabelValues(kind).Observe(duration.Seconds())
}

func outcomeForVerdict(v Verdict) ValidationOutcome {
	switch v {
	case VerdictWithinBudget:
		return OutcomeWithinBudget
	case VerdictOverBudget:
		return OutcomeOverBudget
	default:
		return OutcomeUnknownBounds
	}
}
