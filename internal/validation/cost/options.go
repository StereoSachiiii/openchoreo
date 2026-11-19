// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package cost

import (
	"github.com/openchoreo/openchoreo/internal/limits"
)

// Option configures the cost ceilings of a SpecEstimator or WebhookCostChecker.
type Option func(*budgetConfig)

// WithBudget sets the cumulative render budget. Zero means "derive from the expression limit"
// (limits.ReconcileBudget).
func WithBudget(budget uint64) Option {
	return func(c *budgetConfig) { c.budget = budget }
}

// WithExpressionLimit sets the per-expression cost ceiling. Zero means DefaultCELCostLimit.
func WithExpressionLimit(limit uint64) Option {
	return func(c *budgetConfig) { c.expressionLimit = limit }
}

// budgetConfig holds the two cost ceilings used throughout the package.
type budgetConfig struct {
	expressionLimit uint64
	budget          uint64
}

// resolveBudgetConfig applies opts and fills in defaults:
//   - expressionLimit defaults to DefaultCELCostLimit;
//   - budget defaults to limits.ReconcileBudget(expressionLimit)
//     (MaxCumulativeRenderBudget for the default expression limit).
func resolveBudgetConfig(opts []Option) budgetConfig {
	var c budgetConfig
	for _, opt := range opts {
		opt(&c)
	}
	if c.expressionLimit == 0 {
		c.expressionLimit = DefaultCELCostLimit
	}
	if c.budget == 0 {
		c.budget = limits.ReconcileBudget(c.expressionLimit)
	}
	return c
}
