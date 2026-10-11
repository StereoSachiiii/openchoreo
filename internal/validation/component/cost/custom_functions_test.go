// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package cost_test

import (
	"testing"

	"github.com/google/cel-go/checker"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiservercel "k8s.io/apiserver/pkg/cel"

	"github.com/openchoreo/openchoreo/internal/validation/component"
	"github.com/openchoreo/openchoreo/internal/validation/component/cost"
)

func TestCustomFunctions_CostEstimates(t *testing.T) {
	val, err := component.NewCELValidator(component.ComponentTypeResource, component.SchemaOptions{})
	require.NoError(t, err)
	env := val.GetBaseEnv()

	roots := map[string]*apiservercel.DeclType{
		"metadata": apiservercel.NewObjectType("Metadata", map[string]*apiservercel.DeclField{
			"name": apiservercel.NewDeclField("name", apiservercel.StringType, true, nil, nil),
		}),
	}
	coster := cost.NewDeclTypeCostEstimator(roots)

	t.Run("oc_hash formula matches 1 + ceil(0.1 * bytes)", func(t *testing.T) {
		ast, issues := env.Compile("oc_hash(metadata.name)")
		require.Nil(t, issues)

		estimate, err := checker.Cost(ast.NativeRep(), coster)
		require.NoError(t, err)

		// metadata.name is bound to 253 chars/bytes.
		// AST node breakdown: metadata (1) + .name select (1) + oc_hash base (1) + ceil(253 * 0.1 = 26) = 29.
		assert.Equal(t, uint64(29), estimate.Max)
	})

	t.Run("oc_dns_label bounds result to 63", func(t *testing.T) {
		ast, issues := env.Compile("oc_dns_label(metadata.name)")
		require.Nil(t, issues)

		estimate, err := checker.Cost(ast.NativeRep(), coster)
		require.NoError(t, err)
		assert.True(t, estimate.Max > 0)
	})

	t.Run("oc_omit has constant cost 1", func(t *testing.T) {
		ast, issues := env.Compile("oc_omit()")
		require.Nil(t, issues)

		estimate, err := checker.Cost(ast.NativeRep(), coster)
		require.NoError(t, err)
		assert.Equal(t, uint64(1), estimate.Max)
	})
}
