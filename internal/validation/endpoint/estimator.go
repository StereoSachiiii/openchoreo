// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

// Package endpoint provides CEL environment and cost validation for EndpointType resources.
package endpoint

import (
	"encoding/json"
	"fmt"

	apiextschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/openchoreo/openchoreo/api/v1alpha1"
	"github.com/openchoreo/openchoreo/internal/validation/cost"
)

// EndpointDomainEstimator estimates worst-case CEL cost for EndpointType and ClusterEndpointType.
type EndpointDomainEstimator struct{}

var _ cost.DomainCostEstimator = &EndpointDomainEstimator{}

// Kind returns the resource kind handled by this estimator.
func (e *EndpointDomainEstimator) Kind() string {
	return "EndpointType"
}

// Estimate evaluates the worst-case CEL cost for the endpoint type object.
func (e *EndpointDomainEstimator) Estimate(
	obj runtime.Object,
	schemas cost.SchemaPair,
	opts ...cost.Option,
) (cost.TemplateEstimate, error) {
	var spec v1alpha1.EndpointTypeSpec
	switch res := obj.(type) {
	case *v1alpha1.EndpointType:
		spec = res.Spec
	case *v1alpha1.ClusterEndpointType:
		spec = v1alpha1.EndpointTypeSpec{
			Parameters:         res.Spec.Parameters,
			EnvironmentConfigs: res.Spec.EnvironmentConfigs,
			Validations:        res.Spec.Validations,
			URLs:               res.Spec.URLs,
			Resources:          res.Spec.Resources,
		}
	default:
		return cost.TemplateEstimate{}, fmt.Errorf("unsupported object for EndpointDomainEstimator: %T", obj)
	}

	return EstimateEndpointTypeSpec(spec, schemas.Parameters, schemas.EnvironmentConfigs, opts...)
}

// EstimateEndpointTypeSpec calculates the worst-case CEL cost of an EndpointTypeSpec.
func EstimateEndpointTypeSpec(
	spec v1alpha1.EndpointTypeSpec,
	paramSchema, envConfigSchema *apiextschema.Structural,
	opts ...cost.Option,
) (cost.TemplateEstimate, error) {
	baseEnv, _, roots, err := BuildEndpointCELEnv(SchemaOptions{
		ParametersSchema:         paramSchema,
		EnvironmentConfigsSchema: envConfigSchema,
	})
	if err != nil {
		return cost.TemplateEstimate{}, fmt.Errorf("build endpoint CEL env: %w", err)
	}

	est := cost.NewSpecEstimator(roots, opts...)
	r := est.Resource("endpoint")

	for i, val := range spec.Validations {
		r.AddExpr(baseEnv, cost.ItemPath("spec.validations", i, "")+".rule", val.Rule)
	}

	for i, res := range spec.Resources {
		resPath := cost.ItemPath("spec.resources", i, res.ID)
		r.AddExpr(baseEnv, resPath+".includeWhen", res.IncludeWhen)
		r.AddTemplate(baseEnv, resPath+".template", res.Template)
	}

	// URLs are a typed struct, so marshal them to JSON to walk their string fields.
	if spec.URLs != nil {
		if raw, err := json.Marshal(spec.URLs); err == nil {
			r.AddTemplate(baseEnv, "spec.urls", &runtime.RawExtension{Raw: raw})
		}
	}

	return est.Result(), nil
}
