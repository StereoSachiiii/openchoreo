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
	"github.com/openchoreo/openchoreo/internal/validation/component/cost"
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

	aggregator := cost.NewTemplateAggregator(val)

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

		rEst, err := aggregator.EstimateResource(res)
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

		rEst, err := aggregator.EstimateResource(res)
		require.NoError(t, err)
		assert.Equal(t, uint64(20), rEst.LoopBound)
		assert.Equal(t, cost.StateKnown, rEst.State)
		assert.Equal(t, cost.VerdictWithinBudget, rEst.Verdict)
		assert.True(t, rEst.TotalCost >= 20*rEst.BodyCostPerIter)
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

		rEst, err := aggregator.EstimateResource(res)
		require.NoError(t, err)
		assert.Equal(t, cost.StateUnknown, rEst.State)
		assert.Equal(t, cost.VerdictUnknown, rEst.Verdict)
		assert.Equal(t, cost.MaxForEachItemsPerLoop, rEst.LoopBound)
	})
}
