// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package component

import (
	"fmt"
	"strings"

	"github.com/google/cel-go/cel"
	apiservercel "k8s.io/apiserver/pkg/cel"

	"github.com/openchoreo/openchoreo/internal/validation/cost"
)

// extendForEachEnv returns the CEL environment and loop variable's DeclType for a forEach loop.
func extendForEachEnv(
	baseEnv *cel.Env,
	typeProvider *apiservercel.DeclTypeProvider,
	forEachExpr string,
	varName string,
) (*cel.Env, *apiservercel.DeclType, error) {
	if forEachExpr == "" {
		return baseEnv, nil, nil
	}

	inner := cost.UnwrapCEL(forEachExpr)
	info, err := analyzeForEachExpression(inner, varName, baseEnv)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to analyze forEach expression: %w", err)
	}
	if info == nil {
		return baseEnv, nil, nil
	}

	extEnv, err := extendEnvWithForEach(baseEnv, info, typeProvider)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to extend CEL environment with forEach: %w", err)
	}
	return extEnv, info.VarDeclType, nil
}

// resolveForEachLoop evaluates forEach loop size, bound, and body environment/coster.
func resolveForEachLoop(
	est *cost.SpecEstimator,
	baseEnv *cel.Env,
	typeProvider *apiservercel.DeclTypeProvider,
	resourceID, rawForEach, loopVar, fieldPath string,
) (bodyEnv *cel.Env, loop cost.LoopInfo, err error) {
	bodyEnv = baseEnv
	loop.Bound = 1
	loop.State = cost.StateKnown

	if rawForEach == "" {
		return bodyEnv, loop, nil
	}

	inner := cost.UnwrapCEL(rawForEach)

	// Cost of evaluating the forEach expression itself.
	forEst := est.EstimateExpr(baseEnv, fieldPath, inner)
	loop.Expr = &forEst
	if forEst.State == cost.StateUnknown {
		loop.State = cost.StateUnknown
	}

	// Number of times the body runs.
	bound, capExceeded, boundState := estimateLoopIterations(est, inner, baseEnv)
	loop.Bound = bound
	loop.CapExceeded = capExceeded
	if boundState == cost.StateUnknown {
		loop.State = cost.StateUnknown
	}

	// Body environment with loop variable bound.
	extEnv, loopVarDeclType, err := extendForEachEnv(baseEnv, typeProvider, rawForEach, loopVar)
	if err != nil {
		return baseEnv, loop, fmt.Errorf("failed to get resource environment for %s: %w", resourceID, err)
	}
	bodyEnv = extEnv

	if loopVarDeclType != nil && loopVar != "" {
		loop.BodyCoster = est.Coster().WithRootType(loopVar, loopVarDeclType)
	}

	return bodyEnv, loop, nil
}

// estimateLoopIterations bounds a forEach loop to M = min(sizeEst.Max, MaxForEachItemsPerLoop).
// When sizeEst.Max exceeds MaxForEachItemsPerLoop (either via an explicit maxItems > 250 or an
// unbounded physical payload fallback), it clamps M to 250 with capExceeded = true and StateKnown:
// compute cost remains sound because runtime rejects >250 items, while WebhookCostChecker surfaces
// an actionable admission warning rather than hard-rejecting (avoiding false positives on .filter()
// loops and keeping the cost contract strictly focused on compute budgets).
func estimateLoopIterations(est *cost.SpecEstimator, inner string, baseEnv *cel.Env) (loopBound uint64, capExceeded bool, state cost.SizeState) {
	if ast, _ := baseEnv.Compile(inner); ast == nil {
		return cost.MaxForEachItemsPerLoop, false, cost.StateUnknown
	}

	path, ok := extractForEachCollectionPath(inner)
	if !ok {
		return cost.MaxForEachItemsPerLoop, false, cost.StateUnknown
	}

	sizeEst := est.EstimateSize(path...)
	if sizeEst == nil || sizeEst.Max == 0 {
		return cost.MaxForEachItemsPerLoop, false, cost.StateUnknown
	}
	if sizeEst.Max > cost.MaxForEachItemsPerLoop {
		return cost.MaxForEachItemsPerLoop, true, cost.StateKnown
	}
	return sizeEst.Max, false, cost.StateKnown
}

var collectionTransformCalls = map[string]bool{
	"transformMap":      true,
	"transformMapEntry": true,
	"transformList":     true,
	"filter":            true,
	"map":               true,
	"distinct":          true,
}

func extractForEachCollectionPath(expr string) ([]string, bool) {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return nil, false
	}

	var path []string
	for _, segment := range strings.Split(expr, ".") {
		openParen := strings.IndexByte(segment, '(')
		if openParen == -1 {
			path = append(path, segment)
			continue
		}

		callName := segment[:openParen]
		if collectionTransformCalls[callName] {
			return path, len(path) > 0
		}

		if !strings.HasSuffix(segment, ")") {
			return nil, false
		}
		args := segment[openParen+1 : len(segment)-1]
		if strings.ContainsAny(args, ".()") {
			return nil, false
		}
		path = append(path, callName)
	}

	return path, len(path) > 0
}
