// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package cost_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiextschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/openchoreo/openchoreo/api/v1alpha1"
	"github.com/openchoreo/openchoreo/internal/validation/component"
	"github.com/openchoreo/openchoreo/internal/validation/cost"
)

func TestJudgeRatchet(t *testing.T) {
	const budget uint64 = 100_000

	t.Run("new estimate within budget is always allowed", func(t *testing.T) {
		oldEst := cost.TemplateEstimate{Verdict: cost.VerdictOverBudget, TotalCost: 200_000}
		newEst := cost.TemplateEstimate{Verdict: cost.VerdictWithinBudget, TotalCost: 50_000}

		allowed, warn, viol := cost.JudgeRatchet("ComponentType", "my-comp", oldEst, newEst, budget)
		assert.True(t, allowed)
		assert.Empty(t, warn)
		assert.Empty(t, viol)
	})

	t.Run("rank improvement from Unknown to OverBudget is allowed with warning", func(t *testing.T) {
		oldEst := cost.TemplateEstimate{Verdict: cost.VerdictUnknown}
		newEst := cost.TemplateEstimate{Verdict: cost.VerdictOverBudget, TotalCost: 150_000}

		allowed, warn, viol := cost.JudgeRatchet("ComponentType", "my-comp", oldEst, newEst, budget)
		assert.True(t, allowed)
		assert.Contains(t, warn, "improved cost verdict from UNKNOWN to OVER BUDGET (allowed by ratchet)")
		assert.Empty(t, viol)
	})

	t.Run("retained over budget with non-increasing cost is allowed with warning", func(t *testing.T) {
		oldEst := cost.TemplateEstimate{Verdict: cost.VerdictOverBudget, TotalCost: 200_000}
		newEst := cost.TemplateEstimate{Verdict: cost.VerdictOverBudget, TotalCost: 180_000}

		allowed, warn, viol := cost.JudgeRatchet("ComponentType", "my-comp", oldEst, newEst, budget)
		assert.True(t, allowed)
		assert.Contains(t, warn, "retained cost verdict OVER BUDGET (cost 180000 <= old 200000; allowed by ratchet)")
		assert.Empty(t, viol)
	})

	t.Run("retained over budget with equal cost is allowed with warning", func(t *testing.T) {
		oldEst := cost.TemplateEstimate{Verdict: cost.VerdictOverBudget, TotalCost: 200_000}
		newEst := cost.TemplateEstimate{Verdict: cost.VerdictOverBudget, TotalCost: 200_000}

		allowed, warn, viol := cost.JudgeRatchet("ComponentType", "my-comp", oldEst, newEst, budget)
		assert.True(t, allowed)
		assert.Contains(t, warn, "retained cost verdict OVER BUDGET (cost 200000 <= old 200000; allowed by ratchet)")
		assert.Empty(t, viol)
	})

	t.Run("retained over budget with increased cost is rejected", func(t *testing.T) {
		oldEst := cost.TemplateEstimate{Verdict: cost.VerdictOverBudget, TotalCost: 200_000}
		newEst := cost.TemplateEstimate{Verdict: cost.VerdictOverBudget, TotalCost: 250_000}

		allowed, warn, viol := cost.JudgeRatchet("ComponentType", "my-comp", oldEst, newEst, budget)
		assert.False(t, allowed)
		assert.Empty(t, warn)
		assert.Contains(t, viol, "CEL cost ratchet violated: estimated cost 250000 exceeded budget 100000 (old baseline: 200000)")
	})

	t.Run("retained unknown verdict is allowed with warning", func(t *testing.T) {
		oldEst := cost.TemplateEstimate{Verdict: cost.VerdictUnknown}
		newEst := cost.TemplateEstimate{Verdict: cost.VerdictUnknown}

		allowed, warn, viol := cost.JudgeRatchet("ComponentType", "my-comp", oldEst, newEst, budget)
		assert.True(t, allowed)
		assert.Contains(t, warn, "retained cost verdict UNKNOWN (allowed by ratchet)")
		assert.Empty(t, viol)
	})

	t.Run("degradation to unknown is rejected", func(t *testing.T) {
		oldEst := cost.TemplateEstimate{Verdict: cost.VerdictWithinBudget, TotalCost: 50_000}
		newEst := cost.TemplateEstimate{Verdict: cost.VerdictUnknown}

		allowed, warn, viol := cost.JudgeRatchet("ComponentType", "my-comp", oldEst, newEst, budget)
		assert.False(t, allowed)
		assert.Empty(t, warn)
		assert.Contains(t, viol, "CEL cost ratchet violated: degraded from WITHIN BUDGET to UNKNOWN (unbounded collection or expression introduced)")
	})

	t.Run("degradation from within budget to over budget is rejected", func(t *testing.T) {
		oldEst := cost.TemplateEstimate{Verdict: cost.VerdictWithinBudget, TotalCost: 50_000}
		newEst := cost.TemplateEstimate{Verdict: cost.VerdictOverBudget, TotalCost: 150_000}

		allowed, warn, viol := cost.JudgeRatchet("ComponentType", "my-comp", oldEst, newEst, budget)
		assert.False(t, allowed)
		assert.Empty(t, warn)
		assert.Contains(t, viol, "CEL cost ratchet violated: estimated cost 150000 exceeded budget 100000 (old baseline: 50000)")
	})
}

func TestWebhookCostChecker(t *testing.T) {
	ctx := t.Context()

	t.Run("reports custom budget in diagnostic message", func(t *testing.T) {
		checker := cost.NewWebhookCostChecker(&component.ComponentDomainEstimator{}, cost.ModeEnforce, cost.WithBudget(5))
		body, _ := json.Marshal(map[string]any{
			"apiVersion": "v1",
			"kind":       "ConfigMap",
			"metadata": map[string]any{
				"name": "${metadata.name + '-' + metadata.namespace}",
			},
		})
		ct := &v1alpha1.ComponentType{}
		ct.Name = "custom-budget-ct"
		ct.Spec.Resources = []v1alpha1.ResourceTemplate{
			{
				ID:       "cm",
				Template: &runtime.RawExtension{Raw: body},
			},
		}

		warnings, err := checker.ValidateCreate(ctx, ct, cost.SchemaPair{})
		require.Error(t, err)
		assert.Empty(t, warnings)
		assert.Contains(t, err.Error(), "/ budget 5)")
	})

	t.Run("rejects unsupported runtime.Object type", func(t *testing.T) {
		checker := cost.NewWebhookCostChecker(&component.ComponentDomainEstimator{}, cost.ModeEnforce)
		unsupported := &v1alpha1.Project{}

		_, err := checker.ValidateCreate(ctx, unsupported, cost.SchemaPair{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unsupported object")

		_, err = checker.ValidateUpdate(ctx, unsupported, unsupported, cost.SchemaPair{}, cost.SchemaPair{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unsupported object")
	})

	t.Run("panics when constructed with nil estimator", func(t *testing.T) {
		assert.Panics(t, func() {
			cost.NewWebhookCostChecker(nil, cost.ModeEnforce)
		})
	})

	t.Run("zero-value checker returns explicit error instead of panicking", func(t *testing.T) {
		var zeroChecker cost.WebhookCostChecker
		zeroChecker.Mode = cost.ModeEnforce
		ct := &v1alpha1.ComponentType{}

		_, err := zeroChecker.ValidateCreate(ctx, ct, cost.SchemaPair{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no estimator configured")

		_, err = zeroChecker.ValidateUpdate(ctx, ct, ct, cost.SchemaPair{}, cost.SchemaPair{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no estimator configured")
	})

	t.Run("cross-expression budget trading is rejected by ratchet", func(t *testing.T) {
		oldEst := cost.TemplateEstimate{
			Verdict:   cost.VerdictOverBudget,
			TotalCost: 1_700_000,
			Resources: []cost.ResourceEstimate{
				{
					ID: "r",
					Expressions: []cost.ExpressionEstimate{
						{FieldPath: "resources[r].a", Cost: 1_200_000, Verdict: cost.VerdictOverBudget},
						{FieldPath: "resources[r].b", Cost: 500_000, Verdict: cost.VerdictWithinBudget},
					},
				},
			},
		}
		newEst := cost.TemplateEstimate{
			Verdict:   cost.VerdictOverBudget,
			TotalCost: 1_650_000, // Lower total, but b newly crossed the per-expression limit
			Resources: []cost.ResourceEstimate{
				{
					ID: "r",
					Expressions: []cost.ExpressionEstimate{
						{FieldPath: "resources[r].a", Cost: 600_000, Verdict: cost.VerdictWithinBudget},
						{FieldPath: "resources[r].b", Cost: 1_050_000, Verdict: cost.VerdictOverBudget},
					},
				},
			},
		}

		allowed, _, viol := cost.JudgeRatchet("ComponentType", "trade-ct", oldEst, newEst, 5_000_000)
		assert.False(t, allowed)
		assert.Contains(t, viol, "resources[r].b")
	})

	t.Run("metadata-only update (equal Spec, finalizer/annotation change, even with generation 0) short-circuits estimation", func(t *testing.T) {
		checker := cost.NewWebhookCostChecker(&component.ComponentDomainEstimator{}, cost.ModeEnforce, cost.WithBudget(1))
		body, err := json.Marshal(map[string]any{"data": map[string]any{"a": "${metadata.name + '-x'}"}})
		require.NoError(t, err)

		oldCT := &v1alpha1.ComponentType{}
		oldCT.Name = "legacy-ct"
		oldCT.Finalizers = []string{"openchoreo.dev/cleanup"}
		oldCT.Spec.Resources = []v1alpha1.ResourceTemplate{{ID: "cm", Template: &runtime.RawExtension{Raw: body}}}

		newCT := oldCT.DeepCopy()
		newCT.Finalizers = nil // finalizer removal (generation == 0, no status subresource needed)

		warnings, err := checker.ValidateUpdate(ctx, oldCT, newCT, cost.SchemaPair{}, cost.SchemaPair{})
		assert.NoError(t, err)
		assert.Empty(t, warnings)

		// ArgoCD/Flux tracking-annotation sync with unchanged Spec must also short-circuit
		annotatedCT := oldCT.DeepCopy()
		annotatedCT.Annotations = map[string]string{"argocd.argoproj.io/tracking-id": "app:openchoreo.dev/ComponentType:default/legacy-ct"}
		warnings, err = checker.ValidateUpdate(ctx, oldCT, annotatedCT, cost.SchemaPair{}, cost.SchemaPair{})
		assert.NoError(t, err)
		assert.Empty(t, warnings)
	})

	t.Run("forEach schema bound exceeding per-loop cap emits admission warning", func(t *testing.T) {
		checker := cost.NewWebhookCostChecker(&component.ComponentDomainEstimator{}, cost.ModeEnforce)
		maxItems500 := int64(500)
		paramSchema := &apiextschema.Structural{
			Generic: apiextschema.Generic{Type: "object"},
			Properties: map[string]apiextschema.Structural{
				"tags": {
					Generic:         apiextschema.Generic{Type: "array"},
					ValueValidation: &apiextschema.ValueValidation{MaxItems: &maxItems500},
					Items:           &apiextschema.Structural{Generic: apiextschema.Generic{Type: "string"}},
				},
			},
		}
		ct := &v1alpha1.ComponentType{}
		ct.Name = "foreach-cap-ct"
		ct.Spec.Resources = []v1alpha1.ResourceTemplate{
			{
				ID:       "cm",
				ForEach:  "${parameters.tags}",
				Var:      "t",
				Template: &runtime.RawExtension{Raw: []byte(`{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"${t}"}}`)},
			},
		}

		warnings, err := checker.ValidateCreate(ctx, ct, cost.SchemaPair{Parameters: paramSchema})
		require.NoError(t, err)
		require.NotEmpty(t, warnings)
		assert.Contains(t, warnings[0], `whose schema bound (500) exceeds the runtime per-loop cap (250)`)

		// Unbounded forEach (no maxItems declared) emits a distinct actionable warning
		unboundedSchema := &apiextschema.Structural{
			Generic: apiextschema.Generic{Type: "object"},
			Properties: map[string]apiextschema.Structural{
				"tags": {
					Generic: apiextschema.Generic{Type: "array"},
					Items:   &apiextschema.Structural{Generic: apiextschema.Generic{Type: "string"}},
				},
			},
		}
		warnings, err = checker.ValidateCreate(ctx, ct, cost.SchemaPair{Parameters: unboundedSchema})
		require.NoError(t, err)
		require.NotEmpty(t, warnings)
		assert.Contains(t, warnings[0], `which has no maxItems/maxProperties bound; add maxItems <= 250`)
	})
}
