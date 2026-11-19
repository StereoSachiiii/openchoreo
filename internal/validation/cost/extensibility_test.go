// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package cost_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/cel-go/cel"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiextschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"
	apiservercel "k8s.io/apiserver/pkg/cel"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"github.com/openchoreo/openchoreo/api/v1alpha1"
	"github.com/openchoreo/openchoreo/internal/template"
	"github.com/openchoreo/openchoreo/internal/validation/cost"
	"github.com/openchoreo/openchoreo/internal/validation/cost/costtest"
	"github.com/openchoreo/openchoreo/internal/validation/schemautil"
)

// This file is a runnable blueprint for onboarding a new template pipeline
// (using a hypothetical NetworkPolicyTemplate CRD) to internal/validation/cost.
//
// Onboarding a new pipeline requires four steps and zero changes to internal/validation/cost:
//   1. Build a CEL env and roots map from the pipeline's schemas and context types.
//   2. Implement cost.DomainCostEstimator using cost.NewSpecEstimator
//      (forwarding opts... for dual-budget limits).
//   3. Wire the CRD webhook to hold *cost.WebhookCostChecker via
//      cost.NewWebhookCostChecker(&YourDomainEstimator{}, mode, opts...) and pass
//      both oldSchemas and newSchemas on ValidateUpdate.
//   4. Subscribe to costtest.RunDomainEstimatorContract in your test suite.

// Step 1: Hypothetical NetworkPolicyTemplate CRD representing a separate API pipeline.

type networkPolicyRule struct {
	ID          string
	IncludeWhen string
	Template    *runtime.RawExtension
}

type networkPolicyTemplateSpec struct {
	Parameters *v1alpha1.SchemaSection
	Rules      []networkPolicyRule
}

type networkPolicyTemplate struct {
	metav1.TypeMeta
	metav1.ObjectMeta
	Spec networkPolicyTemplateSpec
}

func (n *networkPolicyTemplate) DeepCopyObject() runtime.Object {
	if n == nil {
		return nil
	}
	cp := *n
	if n.Spec.Rules != nil {
		cp.Spec.Rules = make([]networkPolicyRule, len(n.Spec.Rules))
		copy(cp.Spec.Rules, n.Spec.Rules)
	}
	return &cp
}

func (n *networkPolicyTemplate) GetObjectKind() schema.ObjectKind {
	return &n.TypeMeta
}

// Step 2: Implement cost.DomainCostEstimator for the new pipeline.

type networkPolicyDomainEstimator struct{}

var _ cost.DomainCostEstimator = (*networkPolicyDomainEstimator)(nil)

func (e *networkPolicyDomainEstimator) Kind() string {
	return "NetworkPolicyTemplate"
}

func (e *networkPolicyDomainEstimator) Estimate(
	obj runtime.Object,
	schemas cost.SchemaPair,
	opts ...cost.Option,
) (cost.TemplateEstimate, error) {
	npt, ok := obj.(*networkPolicyTemplate)
	if !ok || npt == nil {
		return cost.TemplateEstimate{}, fmt.Errorf("expected *networkPolicyTemplate, got %T", obj)
	}

	env, roots, err := buildNetworkPolicyCELEnv(schemas.Parameters)
	if err != nil {
		return cost.TemplateEstimate{}, fmt.Errorf("build NetworkPolicyTemplate CEL env: %w", err)
	}

	est := cost.NewSpecEstimator(roots, opts...)
	r := est.Resource("networkpolicy")

	for i, rule := range npt.Spec.Rules {
		rulePath := cost.ItemPath("rules", i, rule.ID)
		r.AddExpr(env, rulePath+".includeWhen", rule.IncludeWhen)
		r.AddTemplate(env, rulePath+".template", rule.Template)
	}

	return est.Result(), nil
}

func buildNetworkPolicyCELEnv(paramSchema *apiextschema.Structural) (*cel.Env, map[string]*apiservercel.DeclType, error) {
	baseEnv, err := cel.NewEnv(template.BaseCELExtensions()...)
	if err != nil {
		return nil, nil, err
	}

	paramType := schemautil.SchemaToDeclType(paramSchema, "Parameters")
	roots := map[string]*apiservercel.DeclType{
		"parameters": paramType,
	}

	provider := apiservercel.NewDeclTypeProvider(paramType)
	providerOpts, err := provider.EnvOptions(baseEnv.CELTypeProvider())
	if err != nil {
		return nil, nil, err
	}

	varOpts := make([]cel.EnvOption, 0, 1+len(providerOpts))
	varOpts = append(varOpts, cel.Variable("parameters", paramType.CelType()))
	varOpts = append(varOpts, providerOpts...)

	env, err := baseEnv.Extend(varOpts...)
	return env, roots, err
}

// Step 3: Webhook validator holding *cost.WebhookCostChecker via constructor injection.

type networkPolicyWebhookValidator struct {
	costChecker *cost.WebhookCostChecker
}

func newNetworkPolicyWebhookValidator(mode cost.Mode, opts ...cost.Option) *networkPolicyWebhookValidator {
	return &networkPolicyWebhookValidator{
		costChecker: cost.NewWebhookCostChecker(&networkPolicyDomainEstimator{}, mode, opts...),
	}
}

func (v *networkPolicyWebhookValidator) ValidateCreate(
	ctx context.Context,
	obj runtime.Object,
) (admission.Warnings, error) {
	npt, ok := obj.(*networkPolicyTemplate)
	if !ok {
		return nil, fmt.Errorf("expected *networkPolicyTemplate, got %T", obj)
	}
	paramSchema, _, errs := schemautil.ExtractAndValidateSchemas(npt.Spec.Parameters, nil, field.NewPath("spec"))
	if len(errs) > 0 {
		return nil, errs.ToAggregate()
	}
	return v.costChecker.ValidateCreate(ctx, npt, cost.SchemaPair{Parameters: paramSchema})
}

func (v *networkPolicyWebhookValidator) ValidateUpdate(
	ctx context.Context,
	oldObj, newObj runtime.Object,
) (admission.Warnings, error) {
	oldNPT, ok := oldObj.(*networkPolicyTemplate)
	if !ok {
		return nil, fmt.Errorf("expected old *networkPolicyTemplate, got %T", oldObj)
	}
	newNPT, ok := newObj.(*networkPolicyTemplate)
	if !ok {
		return nil, fmt.Errorf("expected new *networkPolicyTemplate, got %T", newObj)
	}

	newParamSchema, _, errs := schemautil.ExtractAndValidateSchemas(newNPT.Spec.Parameters, nil, field.NewPath("spec"))
	if len(errs) > 0 {
		return nil, errs.ToAggregate()
	}
	oldParamSchema, _ := schemautil.ExtractSchemasBestEffort(oldNPT.Spec.Parameters, nil)

	return v.costChecker.ValidateUpdate(
		ctx,
		oldNPT,
		newNPT,
		cost.SchemaPair{Parameters: oldParamSchema},
		cost.SchemaPair{Parameters: newParamSchema},
	)
}

// Step 4: Verify full compliance with the universal cost contract and webhook lifecycle.

func TestNewPipelineOnboarding_NetworkPolicyTemplate(t *testing.T) {
	cidrSchema := func(maxItems *int64) (*v1alpha1.SchemaSection, cost.SchemaPair) {
		var raw string
		var vv *apiextschema.ValueValidation
		if maxItems != nil {
			raw = fmt.Sprintf(`{"type":"object","properties":{"cidrs":{"type":"array","maxItems":%d,"items":{"type":"string","maxLength":8}}}}`, *maxItems)
			v := *maxItems
			vv = &apiextschema.ValueValidation{MaxItems: &v}
		} else {
			raw = `{"type":"object","properties":{"cidrs":{"type":"array","items":{"type":"string","maxLength":8}}}}`
		}
		len8 := int64(8)
		structural := &apiextschema.Structural{
			Generic: apiextschema.Generic{Type: "object"},
			Properties: map[string]apiextschema.Structural{
				"cidrs": {
					Generic:         apiextschema.Generic{Type: "array"},
					ValueValidation: vv,
					Items: &apiextschema.Structural{
						Generic:         apiextschema.Generic{Type: "string"},
						ValueValidation: &apiextschema.ValueValidation{MaxLength: &len8},
					},
				},
			},
		}
		sec := &v1alpha1.SchemaSection{
			OpenAPIV3Schema: &runtime.RawExtension{Raw: []byte(raw)},
		}
		return sec, cost.SchemaPair{Parameters: structural}
	}

	// Quadratic rule expression whose static cost scales with parameters.cidrs maxItems^2.
	const quadRuleExpr = `${string(size(parameters.cidrs.map(a, parameters.cidrs.map(b, a + b))))}`

	newNPT := func(name string, params *v1alpha1.SchemaSection) *networkPolicyTemplate {
		return &networkPolicyTemplate{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec: networkPolicyTemplateSpec{
				Parameters: params,
				Rules: []networkPolicyRule{
					{
						ID: "ingress-allow",
						Template: &runtime.RawExtension{
							Raw: []byte(fmt.Sprintf(`{"ingress":%q}`, quadRuleExpr)),
						},
					},
				},
			},
		}
	}

	nSmall := int64(5)
	nBetween := int64(300)
	nHuge := int64(2000)

	secSmall, pairSmall := cidrSchema(&nSmall)
	secBetween, pairBetween := cidrSchema(&nBetween)
	secHuge, pairHuge := cidrSchema(&nHuge)
	secUnbounded, pairUnbounded := cidrSchema(nil)

	// Run the universal 9-invariant contract suite against the new pipeline's estimator.
	costtest.RunDomainEstimatorContract(t, costtest.DomainContractSpec{
		Estimator: &networkPolicyDomainEstimator{},
		WithinBudget: costtest.ObjectWithSchemas{
			Object:  newNPT("np-within", secSmall),
			Schemas: pairSmall,
		},
		OverBudget: costtest.ObjectWithSchemas{
			Object:  newNPT("np-over", secHuge),
			Schemas: pairHuge,
		},
		UnboundedIterated: costtest.ObjectWithSchemas{
			Object:  newNPT("np-unbounded", secUnbounded),
			Schemas: pairUnbounded,
		},
		RelaxedSchema: costtest.ObjectWithSchemas{
			Object:  newNPT("np-within", secHuge),
			Schemas: pairHuge,
		},
		Between1MAnd2M: costtest.ObjectWithSchemas{
			Object:  newNPT("np-between", secBetween),
			Schemas: pairBetween,
		},
		UnsupportedObj: &v1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "other"}},
	})

	// End-to-end webhook verification showing create, schema-relaxation ratchet, and custom limits.
	t.Run("Webhook_EndToEnd_Onboarding", func(t *testing.T) {
		ctx := t.Context()

		oldObj := newNPT("np-webhook", secSmall)
		relaxedObj := newNPT("np-webhook", secHuge)
		betweenObj := newNPT("np-between", secBetween)

		validator := newNetworkPolicyWebhookValidator(cost.ModeEnforce)

		// 1. Bounded create admits cleanly.
		warnings, err := validator.ValidateCreate(ctx, oldObj)
		require.NoError(t, err)
		assert.Empty(t, warnings)

		// 2. Relaxing only the parameter schema on update must be rejected by the ratchet.
		_, err = validator.ValidateUpdate(ctx, oldObj, relaxedObj)
		require.Error(t, err, "relaxing schema bounds on update must not bypass the cost ratchet")
		assert.Contains(t, err.Error(), "NetworkPolicyTemplate")

		// 3. Custom WithExpressionLimit option must forward through the webhook into the estimator.
		_, err = validator.ValidateCreate(ctx, betweenObj)
		require.Error(t, err, "default 1M per-expression limit must reject 1M-2M template")

		raisedValidator := newNetworkPolicyWebhookValidator(cost.ModeEnforce, cost.WithExpressionLimit(2_000_000))
		warnings, err = raisedValidator.ValidateCreate(ctx, betweenObj)
		require.NoError(t, err, "custom WithExpressionLimit(2_000_000) must admit 1M-2M template")
		assert.Empty(t, warnings)
	})
}
