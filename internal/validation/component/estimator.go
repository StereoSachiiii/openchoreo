// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

// Package component provides CEL validation and cost estimation for ComponentType, Trait, and related templates.
package component

import (
	"fmt"

	"github.com/google/cel-go/cel"
	apiextschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	"k8s.io/apimachinery/pkg/runtime"
	apiservercel "k8s.io/apiserver/pkg/cel"

	"github.com/openchoreo/openchoreo/api/v1alpha1"
	"github.com/openchoreo/openchoreo/internal/validation/cost"
)

// ComponentDomainEstimator estimates worst-case CEL cost for ComponentType and ClusterComponentType.
type ComponentDomainEstimator struct{}

var _ cost.DomainCostEstimator = &ComponentDomainEstimator{}

// Kind returns the resource kind handled by this estimator.
func (e *ComponentDomainEstimator) Kind() string {
	return "ComponentType"
}

// Estimate evaluates the worst-case CEL cost for the component type object.
func (e *ComponentDomainEstimator) Estimate(
	obj runtime.Object,
	schemas cost.SchemaPair,
	opts ...cost.Option,
) (cost.TemplateEstimate, error) {
	var spec v1alpha1.ComponentTypeSpec
	switch res := obj.(type) {
	case *v1alpha1.ComponentType:
		spec = res.Spec
	case *v1alpha1.ClusterComponentType:
		spec = res.Spec.ToComponentTypeSpec()
	default:
		return cost.TemplateEstimate{}, fmt.Errorf("unsupported object for ComponentDomainEstimator: %T", obj)
	}

	return EstimateComponentTypeSpec(spec, schemas.Parameters, schemas.EnvironmentConfigs, opts...)
}

// TraitDomainEstimator estimates worst-case CEL cost for Trait and ClusterTrait.
type TraitDomainEstimator struct{}

var _ cost.DomainCostEstimator = &TraitDomainEstimator{}

// Kind returns the resource kind handled by this estimator.
func (e *TraitDomainEstimator) Kind() string {
	return "Trait"
}

// Estimate evaluates the worst-case CEL cost for the trait object.
func (e *TraitDomainEstimator) Estimate(
	obj runtime.Object,
	schemas cost.SchemaPair,
	opts ...cost.Option,
) (cost.TemplateEstimate, error) {
	var spec v1alpha1.TraitSpec
	switch res := obj.(type) {
	case *v1alpha1.Trait:
		spec = res.Spec
	case *v1alpha1.ClusterTrait:
		spec = res.Spec.ToTraitSpec()
	default:
		return cost.TemplateEstimate{}, fmt.Errorf("unsupported object for TraitDomainEstimator: %T", obj)
	}

	return EstimateTraitSpec(spec, schemas.Parameters, schemas.EnvironmentConfigs, opts...)
}

// EstimateComponentTypeSpec evaluates worst-case CEL cost for a ComponentTypeSpec.
func EstimateComponentTypeSpec(
	spec v1alpha1.ComponentTypeSpec,
	paramSchema, envConfigSchema *apiextschema.Structural,
	opts ...cost.Option,
) (cost.TemplateEstimate, error) {
	baseEnv, typeProvider, roots, err := BuildComponentCELEnvWithRoots(SchemaOptions{
		ParametersSchema:         paramSchema,
		EnvironmentConfigsSchema: envConfigSchema,
	})
	if err != nil {
		return cost.TemplateEstimate{}, fmt.Errorf("build component CEL env: %w", err)
	}

	est := cost.NewSpecEstimator(roots, opts...)

	// 1. Validations and PreRenderValidations
	//nolint:staticcheck // SA1019: spec.Validations retained for backward compatibility
	allValidations := append(append([]v1alpha1.ValidationRule(nil), spec.Validations...), spec.PreRenderValidations...)
	for i, v := range allValidations {
		est.AddValidation(baseEnv, cost.ItemPath("validations", i, ""), v.Rule)
	}

	// 2. Resources
	for _, res := range spec.Resources {
		if err := addResourceTemplate(est, baseEnv, typeProvider, res); err != nil {
			return est.Result(), err
		}
	}

	// 3. PostRenderValidations
	for i, prv := range spec.PostRenderValidations {
		if err := addPostRenderValidation(est, baseEnv, typeProvider, prv, i); err != nil {
			return est.Result(), err
		}
	}

	// 4. Embedded Traits
	for i, tr := range spec.Traits {
		addEmbeddedTrait(est, baseEnv, tr, i)
	}

	return est.Result(), nil
}

// EstimateTraitSpec evaluates worst-case CEL cost for a TraitSpec.
func EstimateTraitSpec(
	spec v1alpha1.TraitSpec,
	paramSchema, envConfigSchema *apiextschema.Structural,
	opts ...cost.Option,
) (cost.TemplateEstimate, error) {
	baseEnv, typeProvider, roots, err := BuildTraitCELEnvWithRoots(SchemaOptions{
		ParametersSchema:         paramSchema,
		EnvironmentConfigsSchema: envConfigSchema,
	})
	if err != nil {
		return cost.TemplateEstimate{}, fmt.Errorf("build trait CEL env: %w", err)
	}

	est := cost.NewSpecEstimator(roots, opts...)

	// 1. Validations and PreRenderValidations
	//nolint:staticcheck // SA1019: spec.Validations retained for backward compatibility
	allValidations := append(append([]v1alpha1.ValidationRule(nil), spec.Validations...), spec.PreRenderValidations...)
	for i, v := range allValidations {
		est.AddValidation(baseEnv, cost.ItemPath("validations", i, ""), v.Rule)
	}

	// 2. Creates
	for i, create := range spec.Creates {
		res := v1alpha1.ResourceTemplate{
			ID:          cost.ItemPath("creates", i, ""),
			IncludeWhen: create.IncludeWhen,
			ForEach:     create.ForEach,
			Var:         create.Var,
			Template:    create.Template,
		}
		if err := addResourceTemplate(est, baseEnv, typeProvider, res); err != nil {
			return est.Result(), err
		}
	}

	// 3. Patches
	for i, patch := range spec.Patches {
		if err := addTraitPatch(est, baseEnv, typeProvider, patch, cost.ItemPath("patches", i, "")); err != nil {
			return est.Result(), err
		}
	}

	// 4. Removes
	for i, remove := range spec.Removes {
		if err := addTraitRemove(est, baseEnv, typeProvider, remove, cost.ItemPath("removes", i, "")); err != nil {
			return est.Result(), err
		}
	}

	// 5. PostRenderValidations
	for i, prv := range spec.PostRenderValidations {
		if err := addPostRenderValidation(est, baseEnv, typeProvider, prv, i); err != nil {
			return est.Result(), err
		}
	}

	return est.Result(), nil
}

// EstimateResource computes the worst-case cost for a single resource template.
func EstimateResource(val *CELValidator, res v1alpha1.ResourceTemplate, opts ...cost.Option) (cost.ResourceEstimate, error) {
	est := cost.NewSpecEstimator(val.GetRootTypes(), opts...)
	if err := addResourceTemplate(est, val.baseEnv, val.typeProvider, res); err != nil {
		return cost.ResourceEstimate{ID: res.ID}, err
	}
	result := est.Result()
	if len(result.Resources) > 0 {
		return result.Resources[0], nil
	}
	return cost.ResourceEstimate{ID: res.ID}, nil
}

// EstimateExpression estimates a single CEL expression in a given environment.
func EstimateExpression(val *CELValidator, expr string, env *cel.Env, fieldPath string, opts ...cost.Option) cost.ExpressionEstimate {
	est := cost.NewSpecEstimator(val.GetRootTypes(), opts...)
	return est.EstimateExpr(env, fieldPath, expr)
}

func addResourceTemplate(
	est *cost.SpecEstimator,
	baseEnv *cel.Env,
	typeProvider *apiservercel.DeclTypeProvider,
	res v1alpha1.ResourceTemplate,
) error {
	resPath := "resources[" + res.ID + "]"
	r := est.Resource(res.ID)
	r.AddWhen(baseEnv, resPath+".includeWhen", res.IncludeWhen)

	bodyEnv, loop, err := resolveForEachLoop(est, baseEnv, typeProvider, res.ID, res.ForEach, res.Var, resPath+".forEach")
	if err != nil {
		return err
	}
	r.SetLoop(loop)
	r.AddTemplate(bodyEnv, resPath+".template", res.Template)
	return nil
}

func addTraitPatch(
	est *cost.SpecEstimator,
	baseEnv *cel.Env,
	typeProvider *apiservercel.DeclTypeProvider,
	patch v1alpha1.TraitPatch,
	id string,
) error {
	r := est.Resource(id)
	bodyEnv, loop, err := resolveForEachLoop(est, baseEnv, typeProvider, id, patch.ForEach, patch.Var, id+".forEach")
	if err != nil {
		return err
	}
	r.SetLoop(loop)

	if patch.Target.Where != "" {
		whereEnv, err := bodyEnv.Extend(cel.Variable("resource", cel.DynType))
		if err != nil {
			return err
		}
		r.AddExpr(whereEnv, id+".target.where", patch.Target.Where)
	}

	for opIdx, op := range patch.Operations {
		opPath := cost.ItemPath(id+".operations", opIdx, "") + ".value"
		r.AddTemplate(bodyEnv, opPath, op.Value)
	}
	return nil
}

func addTraitRemove(
	est *cost.SpecEstimator,
	baseEnv *cel.Env,
	typeProvider *apiservercel.DeclTypeProvider,
	remove v1alpha1.TraitRemove,
	id string,
) error {
	r := est.Resource(id)
	bodyEnv, loop, err := resolveForEachLoop(est, baseEnv, typeProvider, id, remove.ForEach, remove.Var, id+".forEach")
	if err != nil {
		return err
	}
	r.SetLoop(loop)

	if remove.Target.Where != "" {
		whereEnv, err := bodyEnv.Extend(cel.Variable("resource", cel.DynType))
		if err != nil {
			return err
		}
		r.AddExpr(whereEnv, id+".target.where", remove.Target.Where)
	}
	return nil
}

func addPostRenderValidation(
	est *cost.SpecEstimator,
	baseEnv *cel.Env,
	typeProvider *apiservercel.DeclTypeProvider,
	prv v1alpha1.PostRenderValidation,
	idx int,
) error {
	id := cost.ItemPath("postRenderValidations", idx, "")
	r := est.Resource(id)
	r.AddWhen(baseEnv, id+".when", prv.When)

	bodyEnv, loop, err := resolveForEachLoop(est, baseEnv, typeProvider, id, prv.ForEach, prv.Var, id+".forEach")
	if err != nil {
		return err
	}
	r.SetLoop(loop)

	ruleEnv, err := bodyEnv.Extend(cel.Variable("resource", cel.DynType))
	if err != nil {
		return fmt.Errorf("failed to extend environment with resource for %s: %w", id, err)
	}

	if prv.Target.Where != "" {
		r.AddExpr(ruleEnv, id+".target.where", prv.Target.Where)
	}
	if prv.Rule != "" {
		r.AddExpr(ruleEnv, id+".rule", prv.Rule)
	}
	return nil
}

func addEmbeddedTrait(
	est *cost.SpecEstimator,
	baseEnv *cel.Env,
	trait v1alpha1.ComponentTypeTrait,
	idx int,
) {
	traitPath := cost.ItemPath("traits", idx, "")
	r := est.Resource(traitPath)
	r.AddTemplate(baseEnv, traitPath+".parameters", trait.Parameters)
	r.AddTemplate(baseEnv, traitPath+".environmentConfigs", trait.EnvironmentConfigs)
}
