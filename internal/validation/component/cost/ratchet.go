// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package cost

import (
	"fmt"

	"github.com/openchoreo/openchoreo/api/v1alpha1"
)

// RatchetRank assigns an ordinal rank for ratchet comparisons per Project 701 §4.7:
// WITHIN (1) < OVER (2) < UNKNOWN (3).
type RatchetRank int

const (
	RankWithinBudget RatchetRank = 1
	RankOverBudget   RatchetRank = 2
	RankUnknown      RatchetRank = 3
)

// VerdictRank returns the numeric comparison rank for a Verdict.
func VerdictRank(v Verdict) RatchetRank {
	switch v {
	case VerdictWithinBudget:
		return RankWithinBudget
	case VerdictOverBudget:
		return RankOverBudget
	default:
		return RankUnknown
	}
}

// ResourceRatchetResult contains the evaluation of a single resource id during an update.
type ResourceRatchetResult struct {
	ResourceID string
	IsNew      bool
	Passed     bool
	OldVerdict Verdict
	NewVerdict Verdict
	OldCost    uint64
	NewCost    uint64
	Reason     string
}

// RatchetVerdict contains the overall outcome of the ratchet evaluation for a template update.
type RatchetVerdict struct {
	Allowed   bool
	Results   map[string]ResourceRatchetResult
	Violations []string
	Warnings  []string
}

// RatchetEvaluator evaluates resource admission ratchets between an old and new template spec.
type RatchetEvaluator struct {
	aggregator *TemplateAggregator
}

// NewRatchetEvaluator creates a new RatchetEvaluator.
func NewRatchetEvaluator(aggregator *TemplateAggregator) *RatchetEvaluator {
	return &RatchetEvaluator{
		aggregator: aggregator,
	}
}

// EvaluateRatchet compares old and new ComponentType specs per Project 701 §4.7 rules.
func (r *RatchetEvaluator) EvaluateRatchet(oldSpec, newSpec *v1alpha1.ComponentTypeSpec) (RatchetVerdict, error) {
	out := RatchetVerdict{
		Allowed: true,
		Results: make(map[string]ResourceRatchetResult),
	}

	// 1. Recompute old estimates from stored oldSpec
	oldEstimates := make(map[string]ResourceEstimate)
	if oldSpec != nil {
		for _, res := range oldSpec.Resources {
			est, err := r.aggregator.EstimateResource(res)
			if err != nil {
				return out, fmt.Errorf("failed to estimate old resource %s: %w", res.ID, err)
			}
			oldEstimates[res.ID] = est
		}
	}

	// 2. Recompute new estimates from newSpec
	newEstimates := make(map[string]ResourceEstimate)
	if newSpec != nil {
		for _, res := range newSpec.Resources {
			est, err := r.aggregator.EstimateResource(res)
			if err != nil {
				return out, fmt.Errorf("failed to estimate new resource %s: %w", res.ID, err)
			}
			newEstimates[res.ID] = est
		}
	}

	// 3. Compare each resource id in newSpec against oldSpec
	for id, newEst := range newEstimates {
		oldEst, exists := oldEstimates[id]
		if !exists {
			// New resource id: applies create rules (must be WITHIN or allowed with warning if enforce mode)
			passed := (newEst.Verdict == VerdictWithinBudget)
			reason := "new resource: must be within budget"
			if !passed {
				reason = fmt.Sprintf("new resource %s has verdict %s", id, newEst.Verdict)
				out.Violations = append(out.Violations, fmt.Sprintf("resource %s: %s", id, reason))
				out.Allowed = false
			}
			out.Results[id] = ResourceRatchetResult{
				ResourceID: id,
				IsNew:      true,
				Passed:     passed,
				NewVerdict: newEst.Verdict,
				NewCost:    newEst.TotalCost,
				Reason:     reason,
			}
			continue
		}

		// Existing resource id: compare according to §4.7 rules
		oldRank := VerdictRank(oldEst.Verdict)
		newRank := VerdictRank(newEst.Verdict)

		passed := false
		var reason string

		switch {
		case newEst.Verdict == VerdictWithinBudget:
			// Rule 1: new is WITHIN
			passed = true
			reason = "new estimate is within budget"

		case newRank < oldRank:
			// Rule 4: new rank < old rank (e.g. was UNKNOWN, now OVER)
			passed = true
			reason = fmt.Sprintf("verdict rank improved from %s to %s", oldEst.Verdict, newEst.Verdict)

		case newEst.Verdict == VerdictOverBudget && oldEst.Verdict == VerdictOverBudget:
			// Rule 2: both OVER and cost_new <= cost_old
			if newEst.TotalCost <= oldEst.TotalCost {
				passed = true
				delta := int64(newEst.TotalCost) - int64(oldEst.TotalCost)
				reason = fmt.Sprintf("cost not increased (delta: %d <= 0)", delta)
			} else {
				passed = false
				reason = fmt.Sprintf("over-budget cost increased from %d to %d", oldEst.TotalCost, newEst.TotalCost)
			}

		case newEst.Verdict == VerdictUnknown && oldEst.Verdict == VerdictUnknown:
			// Rule 3: both UNKNOWN
			passed = true
			reason = "retained UNKNOWN status without introducing higher violation"

		default:
			passed = false
			reason = fmt.Sprintf("verdict degraded from %s to %s", oldEst.Verdict, newEst.Verdict)
		}

		if !passed {
			out.Allowed = false
			out.Violations = append(out.Violations, fmt.Sprintf("resource %s: %s", id, reason))
		} else if newEst.Verdict != VerdictWithinBudget {
			out.Warnings = append(out.Warnings, fmt.Sprintf("resource %s: %s (allowed by ratchet)", id, reason))
		}

		out.Results[id] = ResourceRatchetResult{
			ResourceID: id,
			IsNew:      false,
			Passed:     passed,
			OldVerdict: oldEst.Verdict,
			NewVerdict: newEst.Verdict,
			OldCost:    oldEst.TotalCost,
			NewCost:    newEst.TotalCost,
			Reason:     reason,
		}
	}

	return out, nil
}
