// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package workflow

import (
	"testing"

	"github.com/stretchr/testify/require"
	apiextschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/openchoreo/openchoreo/api/v1alpha1"
)

func TestEstimateWorkflowSpec(t *testing.T) {
	spec := v1alpha1.WorkflowSpec{
		RunTemplate: &runtime.RawExtension{
			Raw: []byte(`{"apiVersion": "batch/v1", "kind": "Job", "spec": {"template": {"spec": {"containers": [{"name": "worker", "image": "${parameters.image}"}]}}}}`),
		},
	}

	paramSchema := &apiextschema.Structural{
		Generic: apiextschema.Generic{
			Type: "object",
		},
		Properties: map[string]apiextschema.Structural{
			"image": {
				Generic: apiextschema.Generic{
					Type: "string",
				},
			},
		},
	}

	est, err := EstimateWorkflowSpec(spec, paramSchema)
	require.NoError(t, err)
	require.Greater(t, est.TotalCost, uint64(0))
}
