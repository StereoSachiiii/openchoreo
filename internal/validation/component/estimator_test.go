// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package component_test

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

func TestTemplateAggregator_ForEachAndBudget(t *testing.T) {
	// 1. Structural schema for parameters with bound maxItems=20
	maxItems20 := int64(20)
	paramSchema := &apiextschema.Structural{
		Generic: apiextschema.Generic{
			Type: "object",
		},
		Properties: map[string]apiextschema.Structural{
			"replicas": {
				Generic: apiextschema.Generic{
					Type: "integer",
				},
			},
			"tags": {
				Generic: apiextschema.Generic{
					Type: "array",
				},
				ValueValidation: &apiextschema.ValueValidation{
					MaxItems: &maxItems20,
				},
				Items: &apiextschema.Structural{
					Generic: apiextschema.Generic{
						Type: "string",
					},
				},
			},
		},
	}

	val, err := component.NewCELValidator(component.ComponentTypeResource, component.SchemaOptions{
		ParametersSchema: paramSchema,
	})
	require.NoError(t, err)

	t.Run("interpolations sum with base charge", func(t *testing.T) {
		body := map[string]any{
			"apiVersion": "v1",
			"kind":       "ConfigMap",
			"metadata": map[string]any{
				"name": "${metadata.name}-${parameters.replicas}",
			},
		}
		raw, _ := json.Marshal(body)

		res := v1alpha1.ResourceTemplate{
			ID: "config",
			Template: &runtime.RawExtension{
				Raw: raw,
			},
		}

		rEst, err := component.EstimateResource(val, res)
		require.NoError(t, err)
		assert.Equal(t, cost.StateKnown, rEst.State)
		assert.Equal(t, cost.VerdictWithinBudget, rEst.Verdict)
		// Metadata.name + parameters.replicas + 2 base charges
		assert.True(t, rEst.TotalCost > 0)
	})

	t.Run("forEach loop bound scales body cost", func(t *testing.T) {
		body := map[string]any{
			"apiVersion": "v1",
			"kind":       "ConfigMap",
			"metadata": map[string]any{
				"name": "tag-${tag}",
			},
		}
		raw, _ := json.Marshal(body)

		res := v1alpha1.ResourceTemplate{
			ID:      "tags-loop",
			ForEach: "${parameters.tags}",
			Var:     "tag",
			Template: &runtime.RawExtension{
				Raw: raw,
			},
		}

		rEst, err := component.EstimateResource(val, res)
		require.NoError(t, err)
		assert.Equal(t, uint64(20), rEst.LoopBound)
		assert.Equal(t, cost.StateKnown, rEst.State)
		assert.Equal(t, cost.VerdictWithinBudget, rEst.Verdict)
		assert.True(t, rEst.TotalCost > 0)
	})

	t.Run("unbounded forEach evaluates to UNKNOWN", func(t *testing.T) {
		body := map[string]any{
			"apiVersion": "v1",
			"kind":       "ConfigMap",
			"metadata": map[string]any{
				"name": "ep-${endpoint.key}",
			},
		}
		raw, _ := json.Marshal(body)

		res := v1alpha1.ResourceTemplate{
			ID:      "ep-loop",
			ForEach: "${workload.container.env}",
			Var:     "endpoint",
			Template: &runtime.RawExtension{
				Raw: raw,
			},
		}

		rEst, err := component.EstimateResource(val, res)
		require.NoError(t, err)
		assert.Equal(t, cost.StateUnknown, rEst.State)
		assert.Equal(t, cost.VerdictUnknown, rEst.Verdict)
		assert.Equal(t, cost.MaxForEachItemsPerLoop, rEst.LoopBound)
	})

	t.Run("EstimateComponentTypeSpec includes PostRenderValidations and embedded Traits", func(t *testing.T) {
		traitParamsRaw, _ := json.Marshal(map[string]any{
			"label": "${metadata.name}-${parameters.replicas}",
		})
		traitEnvRaw, _ := json.Marshal(map[string]any{
			"tagCount": "${size(parameters.tags)}",
		})

		spec := v1alpha1.ComponentTypeSpec{
			WorkloadType: "deployment",
			PostRenderValidations: []v1alpha1.PostRenderValidation{
				{
					Rule:    "${resource.kind != ''}",
					Message: "kind must be set",
				},
				{
					ForEach: "${parameters.tags}",
					Var:     "t",
					Rule:    "${t != ''}",
					Message: "tag must not be empty",
				},
			},
			Traits: []v1alpha1.ComponentTypeTrait{
				{
					Name:               "logging",
					InstanceName:       "logging-1",
					Parameters:         &runtime.RawExtension{Raw: traitParamsRaw},
					EnvironmentConfigs: &runtime.RawExtension{Raw: traitEnvRaw},
				},
			},
		}

		est, err := component.EstimateComponentTypeSpec(spec, paramSchema, nil)
		require.NoError(t, err)
		assert.Equal(t, cost.StateKnown, est.State)
		assert.Equal(t, cost.VerdictWithinBudget, est.Verdict)
		assert.Len(t, est.Resources, 3) // 2 post-render validations + 1 embedded trait
		assert.True(t, est.TotalCost > 0)
	})

	t.Run("EstimateTraitSpec includes Target.Where, Removes, and PostRenderValidations", func(t *testing.T) {
		patchOpRaw, _ := json.Marshal(map[string]any{
			"name": "${parameters.replicas}",
		})

		spec := v1alpha1.TraitSpec{
			Patches: []v1alpha1.TraitPatch{
				{
					ForEach: "${parameters.tags}",
					Var:     "t",
					Target: v1alpha1.PatchTarget{
						Kind:    "Deployment",
						Version: "v1",
						Where:   "${resource.metadata.name == t}",
					},
					Operations: []v1alpha1.JSONPatchOperation{
						{
							Op:    "add",
							Path:  "/metadata/labels/${t}",
							Value: &runtime.RawExtension{Raw: patchOpRaw},
						},
					},
				},
			},
			Removes: []v1alpha1.TraitRemove{
				{
					ForEach: "${parameters.tags}",
					Var:     "t",
					Target: v1alpha1.PatchTarget{
						Kind:    "ConfigMap",
						Version: "v1",
						Where:   "${resource.metadata.name == t}",
					},
				},
			},
			PostRenderValidations: []v1alpha1.PostRenderValidation{
				{
					Rule:    "${resource.kind != ''}",
					Message: "kind must be set",
				},
			},
		}

		est, err := component.EstimateTraitSpec(spec, paramSchema, nil)
		require.NoError(t, err)
		assert.Equal(t, cost.StateKnown, est.State)
		assert.Equal(t, cost.VerdictWithinBudget, est.Verdict)
		assert.Len(t, est.Resources, 3) // 1 patch + 1 remove + 1 post-render validation
		assert.True(t, est.TotalCost > 0)
	})
}
