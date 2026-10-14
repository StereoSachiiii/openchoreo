// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package cost_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/openchoreo/openchoreo/api/v1alpha1"
	"github.com/openchoreo/openchoreo/internal/validation/component"
	"github.com/openchoreo/openchoreo/internal/validation/component/cost"
)

func TestRatchetEvaluator(t *testing.T) {
	val, err := component.NewCELValidator(component.ComponentTypeResource, component.SchemaOptions{})
	require.NoError(t, err)

	agg := cost.NewTemplateAggregator(val)
	ratchet := cost.NewRatchetEvaluator(agg)

	makeResource := func(id, nameExpr string) v1alpha1.ResourceTemplate {
		body := map[string]any{
			"apiVersion": "v1",
			"kind":       "ConfigMap",
			"metadata": map[string]any{
				"name": nameExpr,
			},
		}
		raw, _ := json.Marshal(body)
		return v1alpha1.ResourceTemplate{
			ID: id,
			Template: &runtime.RawExtension{
				Raw: raw,
			},
		}
	}

	t.Run("both within budget passes", func(t *testing.T) {
		oldSpec := &v1alpha1.ComponentTypeSpec{
			Resources: []v1alpha1.ResourceTemplate{
				makeResource("res-1", "${metadata.name}"),
			},
		}
		newSpec := &v1alpha1.ComponentTypeSpec{
			Resources: []v1alpha1.ResourceTemplate{
				makeResource("res-1", "${metadata.name}-${metadata.namespace}"),
			},
		}

		verdict, err := ratchet.EvaluateRatchet(oldSpec, newSpec)
		require.NoError(t, err)
		assert.True(t, verdict.Allowed)
		assert.True(t, verdict.Results["res-1"].Passed)
	})

	t.Run("new resource that is within budget passes", func(t *testing.T) {
		oldSpec := &v1alpha1.ComponentTypeSpec{
			Resources: []v1alpha1.ResourceTemplate{
				makeResource("res-1", "${metadata.name}"),
			},
		}
		newSpec := &v1alpha1.ComponentTypeSpec{
			Resources: []v1alpha1.ResourceTemplate{
				makeResource("res-1", "${metadata.name}"),
				makeResource("res-2", "${metadata.namespace}"),
			},
		}

		verdict, err := ratchet.EvaluateRatchet(oldSpec, newSpec)
		require.NoError(t, err)
		assert.True(t, verdict.Allowed)
		assert.True(t, verdict.Results["res-2"].Passed)
		assert.True(t, verdict.Results["res-2"].IsNew)
	})

	t.Run("existing resource remaining UNKNOWN passes with warning", func(t *testing.T) {
		unboundedLoop := func(id string) v1alpha1.ResourceTemplate {
			r := makeResource(id, "${env.name}")
			r.ForEach = "${workload.container.env}"
			r.Var = "env"
			return r
		}

		oldSpec := &v1alpha1.ComponentTypeSpec{
			Resources: []v1alpha1.ResourceTemplate{
				unboundedLoop("res-loop"),
			},
		}
		newSpec := &v1alpha1.ComponentTypeSpec{
			Resources: []v1alpha1.ResourceTemplate{
				unboundedLoop("res-loop"),
			},
		}

		verdict, err := ratchet.EvaluateRatchet(oldSpec, newSpec)
		require.NoError(t, err)
		assert.True(t, verdict.Allowed)
		assert.True(t, verdict.Results["res-loop"].Passed)
		assert.NotEmpty(t, verdict.Warnings)
	})

	t.Run("improving rank from UNKNOWN to WITHIN passes", func(t *testing.T) {
		oldRes := makeResource("res-loop", "${env.name}")
		oldRes.ForEach = "${workload.container.env}"
		oldRes.Var = "env"

		newRes := makeResource("res-loop", "${metadata.name}")

		oldSpec := &v1alpha1.ComponentTypeSpec{
			Resources: []v1alpha1.ResourceTemplate{oldRes},
		}
		newSpec := &v1alpha1.ComponentTypeSpec{
			Resources: []v1alpha1.ResourceTemplate{newRes},
		}

		verdict, err := ratchet.EvaluateRatchet(oldSpec, newSpec)
		require.NoError(t, err)
		assert.True(t, verdict.Allowed)
		assert.True(t, verdict.Results["res-loop"].Passed)
		assert.Equal(t, cost.VerdictWithinBudget, verdict.Results["res-loop"].NewVerdict)
	})
}
