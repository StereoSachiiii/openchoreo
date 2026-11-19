// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

// Package cost implements static and runtime CEL cost evaluation, bounds checking, and ratchets.
package cost

import (
	"github.com/openchoreo/openchoreo/internal/limits"
)

// Runtime render and loop budget constants.
const (
	DefaultCELCostLimit              = limits.DefaultCELCostLimit
	MaxCumulativeRenderBudget        = limits.MaxCumulativeRenderBudget
	MaxForEachItemsPerLoop           = limits.MaxForEachItemsPerLoop
	MaxForEachItemsPerRender         = limits.MaxForEachItemsPerRender
	InterpolationBaseCharge   uint64 = 1
)

const (
	schemaTypeArray        = "array"
	rootParameters         = "parameters"
	rootEnvironmentConfigs = "environmentConfigs"
)

// unboundedCost is the threshold at or above which a cost estimate is treated as
// unbounded: the estimator could not put a meaningful ceiling on it, so the result
// is reported as UNKNOWN rather than as a concrete number.
const unboundedCost uint64 = 1 << 63

// isUnbounded reports whether a cost is too large to be treated as a real estimate.
func isUnbounded(costVal uint64) bool {
	return costVal >= unboundedCost
}

// SizeState represents knowledge certainty.
type SizeState string

const (
	StateKnown   SizeState = "KNOWN"
	StateUnknown SizeState = "UNKNOWN"
)

// Verdict denotes the overall admission verdict.
type Verdict string

const (
	VerdictWithinBudget Verdict = "WITHIN BUDGET"
	VerdictOverBudget   Verdict = "OVER BUDGET"
	VerdictUnknown      Verdict = "UNKNOWN"
)

// BudgetVerdict classifies a cost against a ceiling.
//
// Precedence: UNKNOWN (unknown state or unbounded cost) wins over OVER BUDGET,
// which wins over WITHIN BUDGET. anyOverBudget marks the result over budget even
// when costVal itself fits under the ceiling (e.g. one child expression exceeded
// its own per-expression limit).
func BudgetVerdict(costVal uint64, state SizeState, ceiling uint64, anyOverBudget bool) (SizeState, Verdict) {
	if state == StateUnknown || isUnbounded(costVal) {
		return StateUnknown, VerdictUnknown
	}
	if anyOverBudget || costVal > ceiling {
		return StateKnown, VerdictOverBudget
	}
	return StateKnown, VerdictWithinBudget
}

// ExpressionEstimate holds the static estimate for a single CEL expression.
type ExpressionEstimate struct {
	FieldPath  string
	Expression string
	Cost       uint64
	State      SizeState
	Verdict    Verdict
}

// ResourceEstimate contains the aggregated cost estimate for a single ResourceTemplate.
type ResourceEstimate struct {
	ID                 string
	TotalCost          uint64
	LoopBound          uint64
	ForEachCapExceeded bool
	State              SizeState
	Verdict            Verdict
	Expressions        []ExpressionEstimate
}

// TemplateEstimate contains the aggregate cost analysis for an entire ComponentType or Trait.
type TemplateEstimate struct {
	TotalCost              uint64
	State                  SizeState
	Verdict                Verdict
	Resources              []ResourceEstimate
	ValidationCost         uint64
	TotalForEachExpansions uint64
	ForEachRenderCapBreach bool
}
