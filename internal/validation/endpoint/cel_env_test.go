// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package endpoint_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	apiextschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"

	"github.com/openchoreo/openchoreo/internal/validation/endpoint"
)

func TestBuildEndpointCELEnv(t *testing.T) {
	paramSchema := &apiextschema.Structural{
		Generic: apiextschema.Generic{Type: "object"},
		Properties: map[string]apiextschema.Structural{
			"port": {Generic: apiextschema.Generic{Type: "integer"}},
		},
	}

	env, provider, roots, err := endpoint.BuildEndpointCELEnv(endpoint.SchemaOptions{
		ParametersSchema: paramSchema,
	})
	require.NoError(t, err)
	require.NotNil(t, env)
	require.NotNil(t, provider)
	require.Contains(t, roots, "parameters")
	require.Contains(t, roots, "metadata")
}
