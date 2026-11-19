// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

// Package resource provides CEL environment and cost validation for ResourceType resources.
package resource

import (
	"fmt"

	apiextschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/openchoreo/openchoreo/api/v1alpha1"
	"github.com/openchoreo/openchoreo/internal/validation/cost"
)

// ResourceDomainEstimator estimates worst-case CEL cost for ResourceType and ClusterResourceType.
type ResourceDomainEstimator struct{}

var _ cost.DomainCostEstimator = &ResourceDomainEstimator{}

// Kind returns the resource kind handled by this estimator.
func (e *ResourceDomainEstimator) Kind() string {
	return "ResourceType"
}

// Estimate evaluates the worst-case CEL cost for the resource type object.
func (e *ResourceDomainEstimator) Estimate(
	obj runtime.Object,
	schemas cost.SchemaPair,
	opts ...cost.Option,
) (cost.TemplateEstimate, error) {
	var spec v1alpha1.ResourceTypeSpec
	switch res := obj.(type) {
	case *v1alpha1.ResourceType:
		spec = res.Spec
	case *v1alpha1.ClusterResourceType:
		spec = v1alpha1.ResourceTypeSpec{
			Parameters:         res.Spec.Parameters,
			EnvironmentConfigs: res.Spec.EnvironmentConfigs,
			Resources:          res.Spec.Resources,
			Outputs:            res.Spec.Outputs,
		}
	default:
		return cost.TemplateEstimate{}, fmt.Errorf("unsupported object for ResourceDomainEstimator: %T", obj)
	}

	return EstimateResourceTypeSpec(spec, schemas.Parameters, schemas.EnvironmentConfigs, opts...)
}

// EstimateResourceTypeSpec calculates the worst-case CEL cost of a ResourceTypeSpec.
func EstimateResourceTypeSpec(
	spec v1alpha1.ResourceTypeSpec,
	paramSchema, envConfigSchema *apiextschema.Structural,
	opts ...cost.Option,
) (cost.TemplateEstimate, error) {
	baseEnv, _, roots, err := BuildResourceCELEnvWithRoots(SchemaOptions{
		ParametersSchema:         paramSchema,
		EnvironmentConfigsSchema: envConfigSchema,
	})
	if err != nil {
		return cost.TemplateEstimate{}, fmt.Errorf("build base CEL env: %w", err)
	}

	appliedEnv, err := ExtendEnvWithApplied(baseEnv)
	if err != nil {
		return cost.TemplateEstimate{}, fmt.Errorf("extend CEL env with applied: %w", err)
	}

	est := cost.NewSpecEstimator(roots, opts...)
	r := est.Resource("resource")

	for i, res := range spec.Resources {
		resPath := cost.ItemPath("resources", i, res.ID)
		r.AddExpr(baseEnv, resPath+".includeWhen", res.IncludeWhen)
		r.AddExpr(appliedEnv, resPath+".readyWhen", res.ReadyWhen)
		r.AddTemplate(baseEnv, resPath+".template", res.Template)
	}

	for i, out := range spec.Outputs {
		outPath := cost.ItemPath("outputs", i, out.Name)
		r.AddExpr(appliedEnv, outPath+".value", out.Value)
		if ref := out.SecretKeyRef; ref != nil {
			r.AddExpr(appliedEnv, outPath+".secretKeyRef.name", ref.Name)
			r.AddExpr(appliedEnv, outPath+".secretKeyRef.key", ref.Key)
		}
		if ref := out.ConfigMapKeyRef; ref != nil {
			r.AddExpr(appliedEnv, outPath+".configMapKeyRef.name", ref.Name)
			r.AddExpr(appliedEnv, outPath+".configMapKeyRef.key", ref.Key)
		}
	}

	return est.Result(), nil
}
