// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package cost

import (
	"math"

	"github.com/google/cel-go/checker"

	"github.com/openchoreo/openchoreo/internal/limits"
)

// Assumptions for call argument sizes when the schema or AST cannot give us a bound.
const (
	// defaultStringArgBytes is the size assumed for an oc_* string argument of unknown size.
	defaultStringArgBytes uint64 = uint64(limits.MaxContainerImageLength)

	// defaultMergeArgSize is the entry count assumed for an oc_merge argument of unknown size.
	defaultMergeArgSize uint64 = 50
)

// cel-go's own pricing constants, mirrored so custom function estimates stay consistent.
const (
	stringTraversalCostFactor        = 0.1 // cost units per byte of string traversed
	flattenBaseCost           uint64 = 11  // fixed overhead of a flatten call
)

// EstimateCallCost prices the built-in functions that cel-go undercharges and the OpenChoreo
// oc_* functions. Returning nil falls back to cel-go's own estimate.
func (e *DeclTypeCostEstimator) EstimateCallCost(function, overloadID string, target *checker.AstNode, args []checker.AstNode) *checker.CallEstimate {
	switch function {
	case "oc_hash":
		// Hashes every byte of its input; the result is a fixed 8 bytes.
		bytes := argSizeMax(args, 0, defaultStringArgBytes)
		return callEstimate(1+traversalCost(bytes), 8, 8)

	case "oc_generate_name":
		return nameCallEstimate(args, assumedStringLength)

	case "oc_dns_label":
		return nameCallEstimate(args, dnsLabelMaxLength)

	case "oc_generate_name_limit":
		limit := uint64(limits.MaxKubernetesNameLength)
		if len(args) > 1 {
			return nameCallEstimate(args[1:], limit)
		}
		return nameCallEstimate(args, limit)

	case "oc_merge":
		// Cost and result both grow with the two maps being merged.
		a := argSizeMax(args, 0, defaultMergeArgSize)
		b := argSizeMax(args, 1, defaultMergeArgSize)
		return callEstimate(1+a+b, 0, a+b)

	case "oc_omit":
		return callEstimate(1, 0, 0)

	case "distinct":
		n, ok := targetSizeMax(target)
		if !ok {
			return nil
		}
		return callEstimate(1+n, 0, n)

	case "_+_":
		return estimateStringConcat(overloadID, args)

	case "flatten":
		return estimateFlatten(target)

	case "split":
		return estimateSplit(target, args)

	case "join":
		return estimateJoin(target, args)
	}

	return nil
}

// estimateStringConcat prices overload add_string (`_+_`).
// Upstream cel-go falls back to math.MaxUint64 / 10 (~1.84e18 cost) when either operand's
// size is unknown; we cap unknown operands to the active schema bound or physical object ceiling.
func estimateStringConcat(overloadID string, args []checker.AstNode) *checker.CallEstimate {
	if overloadID != "add_string" {
		return nil
	}
	stringCap := DefaultMaxLength
	if b := limits.ActiveSchemaBounds(); b.MaxLength > 0 {
		stringCap = uint64(b.MaxLength)
	}
	a := argSizeMax(args, 0, stringCap)
	b := argSizeMax(args, 1, stringCap)
	resSize := min(a+b, DefaultMaxLength)
	return callEstimate(1+traversalCost(resSize), 0, resSize)
}

// estimateFlatten prices list.flatten(). Upstream cel-go charges 1 when the target size is unknown;
// we charge flattenBaseCost + N using the active schema bound or physical object ceiling.
func estimateFlatten(target *checker.AstNode) *checker.CallEstimate {
	n, ok := targetSizeMax(target)
	if !ok {
		if b := limits.ActiveSchemaBounds(); b.MaxItems > 0 {
			n = uint64(b.MaxItems)
		} else {
			n = maxItemsInObject(1)
		}
	}
	return callEstimate(flattenBaseCost+n, 0, n)
}

// estimateSplit prices string.split(). The number of parts is bounded by 64 segments
// (or the string length if shorter).
func estimateSplit(target *checker.AstNode, args []checker.AstNode) *checker.CallEstimate {
	strLen := receiverOrArgSizeMax(target, args, uint64(limits.MaxKubernetesNameLength))
	maxParts := max(min(strLen, uint64(64)), 1)
	return callEstimate(1+traversalCost(strLen), 1, maxParts)
}

// estimateJoin prices list.join(). The resulting string length is bounded by DefaultMaxLength
// or the product of list size and element length.
func estimateJoin(target *checker.AstNode, args []checker.AstNode) *checker.CallEstimate {
	listLen := receiverOrArgSizeMax(target, args, 64)
	resSize := min(listLen*uint64(limits.MaxKubernetesNameLength), DefaultMaxLength)
	return callEstimate(1+traversalCost(resSize), 0, resSize)
}

// nameCallEstimate prices oc_generate_name / oc_dns_label: the input is traversed once, and the
// result is truncated to maxResult (or to the input size, if that is smaller).
func nameCallEstimate(args []checker.AstNode, maxResult uint64) *checker.CallEstimate {
	bytes := argSizeMax(args, 0, defaultStringArgBytes)
	return callEstimate(1+bytes+traversalCost(bytes), 0, min(bytes, maxResult))
}

// callEstimate builds an estimate costing between 1 and maxCost whose result is
// between minResult and maxResult in size.
func callEstimate(maxCost, minResult, maxResult uint64) *checker.CallEstimate {
	return &checker.CallEstimate{
		CostEstimate: checker.CostEstimate{Min: 1, Max: maxCost},
		ResultSize:   &checker.SizeEstimate{Min: minResult, Max: maxResult},
	}
}

// argSizeMax returns the statically computed max size of args[i], or fallback if unknown.
func argSizeMax(args []checker.AstNode, i int, fallback uint64) uint64 {
	if i < len(args) && args[i].ComputedSize() != nil {
		return args[i].ComputedSize().Max
	}
	return fallback
}

// targetSizeMax returns the statically computed max size of a method call's receiver.
func targetSizeMax(target *checker.AstNode) (uint64, bool) {
	if target == nil || (*target).ComputedSize() == nil {
		return 0, false
	}
	return (*target).ComputedSize().Max, true
}

// receiverOrArgSizeMax returns the computed max size from the method receiver or first argument.
func receiverOrArgSizeMax(target *checker.AstNode, args []checker.AstNode, fallback uint64) uint64 {
	if n, ok := targetSizeMax(target); ok {
		return n
	}
	return argSizeMax(args, 0, fallback)
}

// traversalCost is cel-go's charge for traversing n bytes of a string: ceil(0.1 * n).
func traversalCost(n uint64) uint64 {
	return uint64(math.Ceil(float64(n) * stringTraversalCostFactor))
}
