// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

// Package costtest provides reusable contract test suites for subscribers to internal/validation/cost.
package costtest

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/openchoreo/openchoreo/internal/validation/cost"
)

// ObjectWithSchemas pairs a runtime object with the structural OpenAPI schemas extracted from it.
type ObjectWithSchemas struct {
	Object  runtime.Object
	Schemas cost.SchemaPair
}

// DomainContractSpec supplies domain fixtures to verify that a DomainCostEstimator
// satisfies every Project 701 admission, ratchet, diagnostic, and dual-budget invariant.
type DomainContractSpec struct {
	// Estimator is the DomainCostEstimator implementation under test.
	Estimator cost.DomainCostEstimator

	// WithinBudget is a valid bounded template that must pass admission at default limits.
	WithinBudget ObjectWithSchemas

	// OverBudget is a bounded template whose cost exceeds DefaultCELCostLimit (1,000,000).
	OverBudget ObjectWithSchemas

	// UnboundedIterated iterates over a parameter collection lacking maxItems so admission
	// rejects it with actionable MissingSchemaBound diagnostics.
	UnboundedIterated ObjectWithSchemas

	// RelaxedSchema carries the same expressions as WithinBudget, but relaxes or removes
	// its schema bound so its cost exceeds budget. ValidateUpdate from WithinBudget to
	// RelaxedSchema must be rejected by the ratchet.
	RelaxedSchema ObjectWithSchemas

	// Between1MAnd2M has a single expression costing strictly more than 1,000,000 and at most
	// 2,000,000 units. It must fail at default limits and pass when celCostLimit is 2,000,000.
	Between1MAnd2M ObjectWithSchemas

	// UnsupportedObj is a runtime.Object belonging to a different resource kind.
	UnsupportedObj runtime.Object
}

// RunDomainEstimatorContract runs the full Project 701 contract suite against spec.Estimator.
// Any subscriber to internal/validation/cost should invoke this from its package tests so CI
// immediately catches missing ratchet, schema, diagnostic, or dual-budget behavior.
func RunDomainEstimatorContract(t *testing.T, spec DomainContractSpec) {
	t.Helper()

	require.NotNil(t, spec.Estimator, "DomainContractSpec.Estimator must not be nil")
	ctx := context.Background()

	newChecker := func(mode cost.Mode, opts ...cost.Option) *cost.WebhookCostChecker {
		return cost.NewWebhookCostChecker(spec.Estimator, mode, opts...)
	}

	t.Run("Metadata_And_UnsupportedObject", func(t *testing.T) {
		assert.NotEmpty(t, spec.Estimator.Kind(),
			"DomainCostEstimator.Kind must return a non-empty kind name")

		_, err := spec.Estimator.Estimate(spec.UnsupportedObj, cost.SchemaPair{})
		assert.Error(t, err,
			"DomainCostEstimator.Estimate must return an error for unrelated resource kinds")
		_, err = spec.Estimator.Estimate(nil, cost.SchemaPair{})
		assert.Error(t, err,
			"DomainCostEstimator.Estimate must return an error for nil")
	})

	t.Run("ValidateCreate_Enforce_AdmitsWithinBudget", func(t *testing.T) {
		checker := newChecker(cost.ModeEnforce)
		warnings, err := checker.ValidateCreate(ctx, spec.WithinBudget.Object, spec.WithinBudget.Schemas)
		assert.NoError(t, err, "bounded template within budget must be admitted in enforce mode")
		assert.Empty(t, warnings)
	})

	t.Run("ValidateCreate_Enforce_RejectsOverBudget", func(t *testing.T) {
		checker := newChecker(cost.ModeEnforce)
		warnings, err := checker.ValidateCreate(ctx, spec.OverBudget.Object, spec.OverBudget.Schemas)
		require.Error(t, err, "over-budget template must be rejected in enforce mode")
		assert.Empty(t, warnings)
		assert.Contains(t, err.Error(), "CEL cost verdict: OVER BUDGET")
		assert.Contains(t, err.Error(), "Violations & Remediation Guidance:")
	})

	t.Run("ValidateCreate_Enforce_RejectsUnboundedWithRemediation", func(t *testing.T) {
		checker := newChecker(cost.ModeEnforce)
		warnings, err := checker.ValidateCreate(ctx, spec.UnboundedIterated.Object, spec.UnboundedIterated.Schemas)
		require.Error(t, err, "iterating over an unbounded parameter collection must be rejected in enforce mode")
		assert.Empty(t, warnings)
		assert.Contains(t, err.Error(), "Missing OpenAPI Schema Constraints Detected:")
		assert.Contains(t, err.Error(), "maxItems")
	})

	t.Run("ValidateCreate_Warn_AdmitsOverBudgetWithWarning", func(t *testing.T) {
		checker := newChecker(cost.ModeWarn)
		warnings, err := checker.ValidateCreate(ctx, spec.OverBudget.Object, spec.OverBudget.Schemas)
		assert.NoError(t, err, "warn mode must admit over-budget template without error")
		require.NotEmpty(t, warnings, "warn mode must emit an admission warning for over-budget template")
		assert.Contains(t, warnings[0], "CEL cost verdict: OVER BUDGET")
	})

	t.Run("ValidateUpdate_Ratchet_RejectsSchemaRelaxation", func(t *testing.T) {
		checker := newChecker(cost.ModeEnforce)
		_, err := checker.ValidateUpdate(
			ctx,
			spec.WithinBudget.Object,
			spec.RelaxedSchema.Object,
			spec.WithinBudget.Schemas,
			spec.RelaxedSchema.Schemas,
		)
		require.Error(t, err,
			"ratchet bypass detected: ValidateUpdate must cost oldObj against oldSchemas and reject schema relaxations that exceed budget")
		assert.Contains(t, err.Error(), "CEL cost ratchet violated")
	})

	t.Run("ValidateUpdate_Ratchet_AllowsImprovementAndRetention", func(t *testing.T) {
		checker := newChecker(cost.ModeEnforce)

		// Improving from OverBudget to WithinBudget must succeed without warning.
		warnings, err := checker.ValidateUpdate(
			ctx,
			spec.OverBudget.Object,
			spec.WithinBudget.Object,
			spec.OverBudget.Schemas,
			spec.WithinBudget.Schemas,
		)
		assert.NoError(t, err, "ratchet must allow improving from OverBudget to WithinBudget")
		assert.Empty(t, warnings)

		// Retaining OverBudget without increasing cost must succeed with a ratchet warning.
		warnings, err = checker.ValidateUpdate(
			ctx,
			spec.OverBudget.Object,
			spec.OverBudget.Object,
			spec.OverBudget.Schemas,
			spec.OverBudget.Schemas,
		)
		assert.NoError(t, err, "ratchet must allow updating an OverBudget template when cost does not increase")
		require.NotEmpty(t, warnings)
		assert.Contains(t, warnings[0], "allowed by ratchet")

		// Fixing a legacy object whose schema failed extraction (empty SchemaPair) must succeed.
		warnings, err = checker.ValidateUpdate(
			ctx,
			spec.UnboundedIterated.Object,
			spec.WithinBudget.Object,
			cost.SchemaPair{},
			spec.WithinBudget.Schemas,
		)
		assert.NoError(t, err, "ratchet must allow replacing a legacy object with an unextractable schema by a valid within-budget template")
		assert.Empty(t, warnings)
	})

	t.Run("CustomCostLimit_ScalesExpressionLimitAndBudget", func(t *testing.T) {
		defaultChecker := newChecker(cost.ModeEnforce, cost.WithExpressionLimit(0))
		_, err := defaultChecker.ValidateCreate(ctx, spec.Between1MAnd2M.Object, spec.Between1MAnd2M.Schemas)
		require.Error(t, err, "fixture Between1MAnd2M must exceed the default 1,000,000 expression ceiling")

		raisedChecker := newChecker(cost.ModeEnforce, cost.WithExpressionLimit(2_000_000))
		warnings, err := raisedChecker.ValidateCreate(ctx, spec.Between1MAnd2M.Object, spec.Between1MAnd2M.Schemas)
		assert.NoError(t, err,
			"dual-budget drift: DomainCostEstimator must honor cost.WithExpressionLimit instead of hardcoding DefaultCELCostLimit")
		assert.Empty(t, warnings)
	})

	t.Run("ContextCancellation_Respected", func(t *testing.T) {
		cancelledCtx, cancel := context.WithCancel(ctx)
		cancel()

		enforceChecker := newChecker(cost.ModeEnforce)
		_, err := enforceChecker.ValidateCreate(cancelledCtx, spec.WithinBudget.Object, spec.WithinBudget.Schemas)
		// Fast evaluations can race with an already-cancelled context in select; accept either clean pass or cancellation error,
		// but never panic or hang.
		if err != nil {
			assert.Contains(t, err.Error(), "cancelled")
		}
	})
}
