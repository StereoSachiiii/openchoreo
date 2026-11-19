// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package cost_test

import (
	"strings"
	"testing"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/checker"
	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiservercel "k8s.io/apiserver/pkg/cel"

	"github.com/openchoreo/openchoreo/internal/template"
	"github.com/openchoreo/openchoreo/internal/validation/component"
	"github.com/openchoreo/openchoreo/internal/validation/cost"
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

	t.Run("oc_generate_name_limit bounds result to max name length", func(t *testing.T) {
		ast, issues := env.Compile("oc_generate_name_limit(52, metadata.name)")
		require.Nil(t, issues)

		estimate, err := checker.Cost(ast.NativeRep(), coster)
		require.NoError(t, err)
		assert.True(t, estimate.Max > 0)
	})

	t.Run("join charges traversal over string slice", func(t *testing.T) {
		ast, issues := env.Compile("['a', 'b', 'c'].join('-')")
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

// TestCustomFunctions_StaticAndRuntimeParity fails CI if any new oc_* function is registered
// in template.BaseCELExtensions without both static (call_cost.go) and runtime (custom_functions.go)
// cost pricing.
func TestCustomFunctions_StaticAndRuntimeParity(t *testing.T) {
	env, err := cel.NewEnv(template.BaseCELExtensions()...)
	require.NoError(t, err)

	staticEstimator := cost.NewDeclTypeCostEstimator(nil)
	runtimeEstimator := template.CustomFunctionCostEstimator()

	// Functions that take no tenant-sized arguments and intentionally fall through to cel-go's O(1) runtime default.
	zeroArgO1RuntimeFunctions := map[string]bool{
		"oc_omit": true,
	}

	sampleMap := types.DefaultTypeAdapter.NativeToValue(map[string]any{"k": "v"})
	sampleRuntimeArgs := map[string][]ref.Val{
		"oc_hash":                {types.String("sample-input")},
		"oc_generate_name":       {types.String("sample-input")},
		"oc_dns_label":           {types.String("sample-input")},
		"oc_generate_name_limit": {types.Int(63), types.String("sample-input")},
		"oc_merge":               {sampleMap, sampleMap},
	}

	var ocFunctions []string
	for name := range env.Functions() {
		if strings.HasPrefix(name, "oc_") {
			ocFunctions = append(ocFunctions, name)
		}
	}
	require.NotEmpty(t, ocFunctions, "expected at least one oc_* function in BaseCELExtensions")

	staticArgs := []checker.AstNode{
		cost.NewTypedAstNode(types.StringType),
		cost.NewTypedAstNode(types.StringType),
	}

	for _, fn := range ocFunctions {
		t.Run(fn, func(t *testing.T) {
			staticEst := staticEstimator.EstimateCallCost(fn, "", nil, staticArgs)
			assert.NotNilf(t, staticEst,
				"unpriced custom CEL function %q in static estimator: add a case in internal/validation/cost/call_cost.go", fn)

			if zeroArgO1RuntimeFunctions[fn] {
				return
			}

			args := sampleRuntimeArgs[fn]
			if len(args) == 0 {
				args = []ref.Val{types.String("sample-input"), types.String("sample-input")}
			}
			runtimeEst := runtimeEstimator.CallCost(fn, "", args, nil)
			assert.NotNilf(t, runtimeEst,
				"unpriced custom CEL function %q in runtime estimator: add a case in internal/template/custom_functions.go (customFunctionCostEstimator.CallCost)", fn)
		})
	}
}
