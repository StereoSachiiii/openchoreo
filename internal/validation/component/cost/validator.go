// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package cost

import (
	"fmt"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/checker"
	apiextschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
)

// Default budget limits for admission-time CEL expressions.
const (
	// DefaultPerExpressionCostBudget is the maximum allowable estimated cost for a single CEL expression.
	DefaultPerExpressionCostBudget uint64 = 10_000

	// DefaultPerResourceCostBudget is the maximum allowable cost across all expressions within one resource template.
	DefaultPerResourceCostBudget uint64 = 50_000

	// DefaultPerComponentTypeCostBudget is the cumulative budget for an entire ComponentType / Trait spec.
	DefaultPerComponentTypeCostBudget uint64 = 200_000
)

// CostValidationResult contains the output of a CEL cost evaluation.
type CostValidationResult struct {
	EstimatedCost uint64
	BudgetLimit   uint64
	Exceeded      bool
	Expression    string
	FieldPath     string
	Message       string
}

// Validator computes admission-time worst-case cost estimates for OpenChoreo templates.
type Validator struct {
	estimator *StructuralCostEstimator
	budget    uint64
}

// NewValidator creates a cost validator with the given structural schemas and per-expression budget.
func NewValidator(parametersSchema, envConfigsSchema *apiextschema.Structural, budget uint64) *Validator {
	if budget == 0 {
		budget = DefaultPerExpressionCostBudget
	}
	return &Validator{
		estimator: NewStructuralCostEstimator(parametersSchema, envConfigsSchema),
		budget:    budget,
	}
}

// ValidateCost checks if a checked CEL AST exceeds the configured worst-case cost budget.
func (v *Validator) ValidateCost(ast *cel.Ast) (CostValidationResult, error) {
	if ast == nil {
		return CostValidationResult{}, fmt.Errorf("ast cannot be nil")
	}

	costEstimate, err := checker.Cost(ast.NativeRep(), v.estimator)
	if err != nil {
		return CostValidationResult{}, fmt.Errorf("failed to estimate CEL cost: %w", err)
	}

	maxCost := costEstimate.Max
	res := CostValidationResult{
		EstimatedCost: maxCost,
		BudgetLimit:   v.budget,
		Exceeded:      maxCost > v.budget,
	}

	if res.Exceeded {
		res.Message = fmt.Sprintf("estimated cost %d exceeds budget %d", maxCost, v.budget)
	}

	return res, nil
}
