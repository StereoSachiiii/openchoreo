// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

// Package workflow provides CEL environment and cost validation for Workflow resources.
package workflow

import (
	"fmt"

	apiextschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/openchoreo/openchoreo/api/v1alpha1"
	"github.com/openchoreo/openchoreo/internal/validation/cost"
)

// WorkflowDomainEstimator estimates worst-case CEL cost for Workflow and ClusterWorkflow.
type WorkflowDomainEstimator struct{}

var _ cost.DomainCostEstimator = &WorkflowDomainEstimator{}

// Kind returns the resource kind handled by this estimator.
func (e *WorkflowDomainEstimator) Kind() string {
	return "Workflow"
}

// Estimate evaluates the worst-case CEL cost for the workflow object.
func (e *WorkflowDomainEstimator) Estimate(
	obj runtime.Object,
	schemas cost.SchemaPair,
	opts ...cost.Option,
) (cost.TemplateEstimate, error) {
	var spec v1alpha1.WorkflowSpec
	switch res := obj.(type) {
	case *v1alpha1.Workflow:
		spec = res.Spec
	case *v1alpha1.ClusterWorkflow:
		spec = v1alpha1.WorkflowSpec{
			Parameters:         res.Spec.Parameters,
			RunTemplate:        res.Spec.RunTemplate,
			Resources:          res.Spec.Resources,
			ExternalRefs:       res.Spec.ExternalRefs,
			TTLAfterCompletion: res.Spec.TTLAfterCompletion,
		}
	default:
		return cost.TemplateEstimate{}, fmt.Errorf("unsupported object for WorkflowDomainEstimator: %T", obj)
	}

	return EstimateWorkflowSpec(spec, schemas.Parameters, opts...)
}

// EstimateWorkflowSpec estimates the total CEL cost across runTemplate and resources in WorkflowSpec.
func EstimateWorkflowSpec(
	spec v1alpha1.WorkflowSpec,
	paramSchema *apiextschema.Structural,
	opts ...cost.Option,
) (cost.TemplateEstimate, error) {
	env, _, roots, err := BuildWorkflowCELEnv(SchemaOptions{
		ParametersSchema: paramSchema,
	})
	if err != nil {
		return cost.TemplateEstimate{}, fmt.Errorf("build workflow CEL env: %w", err)
	}

	est := cost.NewSpecEstimator(roots, opts...)
	r := est.Resource("workflow")

	r.AddTemplate(env, "spec.runTemplate", spec.RunTemplate)

	for i, res := range spec.Resources {
		resPath := cost.ItemPath("spec.resources", i, res.ID)
		r.AddExpr(env, resPath+".includeWhen", res.IncludeWhen)
		r.AddTemplate(env, resPath+".template", res.Template)
	}

	return est.Result(), nil
}
