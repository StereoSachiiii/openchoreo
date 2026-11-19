// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package resource_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiextschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/openchoreo/openchoreo/api/v1alpha1"
	"github.com/openchoreo/openchoreo/internal/validation/cost"
	"github.com/openchoreo/openchoreo/internal/validation/resource"
)

func TestEstimateResourceTypeSpec_WithinBudget(t *testing.T) {
	spec := v1alpha1.ResourceTypeSpec{
		Resources: []v1alpha1.ResourceTypeManifest{
			{
				ID:          "db-secret",
				IncludeWhen: "${dataplane.secretStore != \"\"}",
				ReadyWhen:   "${applied[\"db-secret\"].status.ready == true}",
				Template: &runtime.RawExtension{
					Raw: []byte(`{"apiVersion":"v1","kind":"Secret","metadata":{"name":"db-secret","namespace":"${metadata.namespace}"},"data":{"password":"${metadata.name}"}}`),
				},
			},
		},
		Outputs: []v1alpha1.ResourceTypeOutput{
			{
				Name:  "host",
				Value: "${applied[\"db-secret\"].status.host}",
			},
		},
	}

	est, err := resource.EstimateResourceTypeSpec(spec, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, cost.StateKnown, est.State)
	assert.Equal(t, cost.VerdictWithinBudget, est.Verdict)
	assert.True(t, est.TotalCost > 0)
	assert.True(t, est.TotalCost <= cost.MaxCumulativeRenderBudget)
}

func TestEstimateResourceTypeSpec_OverBudget(t *testing.T) {
	// Structural schema with bounded array matrix of 60 items
	paramSchema := &apiextschema.Structural{
		Generic: apiextschema.Generic{
			Type: "object",
		},
		Properties: map[string]apiextschema.Structural{
			"matrix": {
				Generic: apiextschema.Generic{
					Type: "array",
				},
				ValueValidation: &apiextschema.ValueValidation{
					MaxItems: ptrInt64(60),
				},
				Items: &apiextschema.Structural{
					Generic: apiextschema.Generic{
						Type: "string",
					},
				},
			},
		},
	}

	// Cubic comprehension loop: 60^3 = 216,000 iterations with string concat
	spec := v1alpha1.ResourceTypeSpec{
		Resources: []v1alpha1.ResourceTypeManifest{
			{
				ID: "cubic-db",
				Template: &runtime.RawExtension{
					Raw: []byte(`{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"cm"},"data":{"bomb":"${string(parameters.matrix.map(a, parameters.matrix.map(b, parameters.matrix.map(c, a + b + c))).size())}"}}`),
				},
			},
		},
	}

	est, err := resource.EstimateResourceTypeSpec(spec, paramSchema, nil)
	require.NoError(t, err)
	assert.Equal(t, cost.VerdictOverBudget, est.Verdict)
}

func ptrInt64(v int64) *int64 {
	return &v
}
