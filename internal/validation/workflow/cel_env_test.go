// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package workflow

import (
	"testing"

	"github.com/stretchr/testify/require"
	apiextschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
)

func TestBuildWorkflowCELEnv(t *testing.T) {
	paramSchema := &apiextschema.Structural{
		Generic: apiextschema.Generic{
			Type: "object",
		},
		Properties: map[string]apiextschema.Structural{
			"message": {
				Generic: apiextschema.Generic{
					Type: "string",
				},
			},
		},
	}

	env, provider, roots, err := BuildWorkflowCELEnv(SchemaOptions{
		ParametersSchema: paramSchema,
	})
	require.NoError(t, err)
	require.NotNil(t, env)
	require.NotNil(t, provider)
	require.Contains(t, roots, "parameters")
	require.Contains(t, roots, "metadata")
}
