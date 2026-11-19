// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package endpoint

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/openchoreo/openchoreo/api/v1alpha1"
	"github.com/openchoreo/openchoreo/internal/validation/cost"
)

func TestEstimateEndpointTypeSpec(t *testing.T) {
	spec := v1alpha1.EndpointTypeSpec{
		Validations: []v1alpha1.ValidationRule{
			{
				Rule:    "${size(metadata.name) > 0}",
				Message: "name must be non-empty",
			},
		},
		Resources: []v1alpha1.ResourceTemplate{
			{
				ID:          "httproute",
				IncludeWhen: "${'external' in visibility}",
				Template: &runtime.RawExtension{
					Raw: []byte(`{"apiVersion":"gateway.networking.k8s.io/v1","kind":"HTTPRoute","metadata":{"name":"${metadata.name}"}}`),
				},
			},
		},
	}

	est, err := EstimateEndpointTypeSpec(spec, nil, nil)
	require.NoError(t, err)
	assert.Greater(t, est.TotalCost, uint64(0))
	assert.Equal(t, cost.VerdictWithinBudget, est.Verdict)
}

func TestEstimateEndpointTypeSpec_CustomBudgetAndOverBudget(t *testing.T) {
	// With budget = 1, any valid expression should be marked over-budget
	spec := v1alpha1.EndpointTypeSpec{
		Validations: []v1alpha1.ValidationRule{
			{
				Rule:    "${size(metadata.name) > 0}",
				Message: "name must be non-empty",
			},
		},
		Resources: []v1alpha1.ResourceTemplate{
			{
				ID: "httproute",
				Template: &runtime.RawExtension{
					Raw: []byte(`{"apiVersion":"gateway.networking.k8s.io/v1","kind":"HTTPRoute","metadata":{"name":"${metadata.name}"}}`),
				},
			},
		},
	}

	est, err := EstimateEndpointTypeSpec(spec, nil, nil, cost.WithBudget(1))
	require.NoError(t, err)
	assert.Greater(t, est.TotalCost, uint64(1))
	assert.Equal(t, cost.VerdictOverBudget, est.Verdict)
}

func TestEstimateEndpointTypeSpec_URLsCost(t *testing.T) {
	specWithoutURLs := v1alpha1.EndpointTypeSpec{
		Resources: []v1alpha1.ResourceTemplate{
			{
				ID: "httproute",
				Template: &runtime.RawExtension{
					Raw: []byte(`{"apiVersion":"gateway.networking.k8s.io/v1","kind":"HTTPRoute","metadata":{"name":"${metadata.name}"}}`),
				},
			},
		},
	}
	estBase, err := EstimateEndpointTypeSpec(specWithoutURLs, nil, nil)
	require.NoError(t, err)
	require.Equal(t, cost.VerdictWithinBudget, estBase.Verdict)

	specWithURLs := v1alpha1.EndpointTypeSpec{
		Resources: specWithoutURLs.Resources,
		URLs: &v1alpha1.EndpointTypeURLs{
			External: &v1alpha1.EndpointURLTemplate{
				IncludeWhen: "${'external' in visibility}",
				Scheme:      "https",
				Host:        "${metadata.name}.example.com",
				Path:        "/v1",
			},
		},
	}
	estURLs, err := EstimateEndpointTypeSpec(specWithURLs, nil, nil)
	require.NoError(t, err)
	assert.Greater(t, estURLs.TotalCost, estBase.TotalCost)
	assert.Equal(t, cost.VerdictWithinBudget, estURLs.Verdict)

	// Verify expressions from URLs were captured
	var foundURLHost, foundURLIncludeWhen bool
	for _, res := range estURLs.Resources {
		for _, expr := range res.Expressions {
			if expr.FieldPath == "spec.urls.external.host" {
				foundURLHost = true
			}
			if expr.FieldPath == "spec.urls.external.includeWhen" {
				foundURLIncludeWhen = true
			}
		}
	}
	assert.True(t, foundURLHost, "expected to find spec.urls.external.host in expressions")
	assert.True(t, foundURLIncludeWhen, "expected to find spec.urls.external.includeWhen in expressions")
}
