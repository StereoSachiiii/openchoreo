// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package cost

import (
	"fmt"
)

// RatchetRank assigns an ordinal rank for ratchet comparisons:
// WithinBudget (1) < OverBudget (2) < UnknownBounds (3). Lower rank is strictly better.
type RatchetRank int

const (
	// WithinBudget indicates the template is within the computational budget.
	WithinBudget RatchetRank = 1

	// OverBudget indicates the template exceeds the computational budget.
	OverBudget RatchetRank = 2

	// UnknownBounds indicates the template contains unbounded fields or expressions.
	UnknownBounds RatchetRank = 3
)

// VerdictRank returns the numeric comparison rank for a Verdict.
func VerdictRank(v Verdict) RatchetRank {
	switch v {
	case VerdictWithinBudget:
		return WithinBudget
	case VerdictOverBudget:
		return OverBudget
	default:
		return UnknownBounds
	}
}

// JudgeRatchet evaluates whether an update from oldEst to newEst satisfies the non-increasing cost ratchet:
//  1. WithinBudget: Always allowed.
//  2. Rank Improvement (e.g. UnknownBounds -> OverBudget): Allowed.
//  3. Retained OverBudget: Allowed only if newEst.TotalCost <= oldEst.TotalCost.
//  4. Degradation (e.g. WithinBudget -> OverBudget or newCost > oldCost): Rejected.
func JudgeRatchet(kind, name string, oldEst, newEst TemplateEstimate, budget uint64) (allowed bool, warningMsg string, violationMsg string) {
	if newEst.Verdict == VerdictWithinBudget {
		return true, "", ""
	}

	oldRank := VerdictRank(oldEst.Verdict)
	newRank := VerdictRank(newEst.Verdict)

	if newRank < oldRank {
		warning := fmt.Sprintf("%s %q improved cost verdict from %s to %s (allowed by ratchet)",
			kind, name, oldEst.Verdict, newEst.Verdict)
		return true, warning, ""
	}

	if newEst.Verdict == VerdictOverBudget && oldEst.Verdict == VerdictOverBudget {
		if viol := checkPerExpressionRatchet(kind, name, oldEst, newEst); viol != "" {
			return false, "", viol
		}
		if newEst.TotalCost <= oldEst.TotalCost {
			warning := fmt.Sprintf("%s %q retained cost verdict %s (cost %d <= old %d; allowed by ratchet)",
				kind, name, newEst.Verdict, newEst.TotalCost, oldEst.TotalCost)
			return true, warning, ""
		}
		msg := fmt.Sprintf("%s %q CEL cost ratchet violated: estimated cost %d exceeded budget %d (old baseline: %d)",
			kind, name, newEst.TotalCost, budget, oldEst.TotalCost)
		return false, "", msg
	}

	if newEst.Verdict == VerdictUnknown && oldEst.Verdict == VerdictUnknown {
		warning := fmt.Sprintf("%s %q retained cost verdict %s (allowed by ratchet)",
			kind, name, newEst.Verdict)
		return true, warning, ""
	}

	if newEst.Verdict == VerdictUnknown {
		msg := fmt.Sprintf("%s %q CEL cost ratchet violated: degraded from %s to %s (unbounded collection or expression introduced)",
			kind, name, oldEst.Verdict, newEst.Verdict)
		return false, "", msg
	}

	msg := fmt.Sprintf("%s %q CEL cost ratchet violated: estimated cost %d exceeded budget %d (old baseline: %d)",
		kind, name, newEst.TotalCost, budget, oldEst.TotalCost)
	return false, "", msg
}

// checkPerExpressionRatchet rejects updates that introduce a newly over-budget expression or
// increase the cost of an already over-budget expression, preventing cross-expression budget trading.
func checkPerExpressionRatchet(kind, name string, oldEst, newEst TemplateEstimate) string {
	oldOverByPath := make(map[string]uint64)
	for _, r := range oldEst.Resources {
		for _, expr := range r.Expressions {
			if expr.Verdict == VerdictOverBudget {
				if prev, ok := oldOverByPath[expr.FieldPath]; !ok || expr.Cost > prev {
					oldOverByPath[expr.FieldPath] = expr.Cost
				}
			}
		}
	}

	for _, r := range newEst.Resources {
		for _, expr := range r.Expressions {
			if expr.Verdict != VerdictOverBudget {
				continue
			}
			oldCost, existed := oldOverByPath[expr.FieldPath]
			if !existed {
				return fmt.Sprintf(
					"%s %q CEL cost ratchet violated: expression at %s newly exceeded per-expression cost limit (cost %d)",
					kind, name, expr.FieldPath, expr.Cost,
				)
			}
			if expr.Cost > oldCost {
				return fmt.Sprintf(
					"%s %q CEL cost ratchet violated: expression at %s regressed from %d to %d",
					kind, name, expr.FieldPath, oldCost, expr.Cost,
				)
			}
		}
	}
	return ""
}
