// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package cost_test

import (
	"testing"

	"github.com/google/cel-go/cel"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime"
	apiservercel "k8s.io/apiserver/pkg/cel"

	"github.com/openchoreo/openchoreo/internal/template"
	"github.com/openchoreo/openchoreo/internal/validation/cost"
)

func TestSpecEstimator_LoopFree(t *testing.T) {
	env, err := cel.NewEnv(template.BaseCELExtensions()...)
	require.NoError(t, err)

	roots := map[string]*apiservercel.DeclType{
		"parameters": apiservercel.NewObjectType("Parameters", map[string]*apiservercel.DeclField{
			"replicas": {Type: apiservercel.IntType},
			"name":     {Type: apiservercel.StringType},
		}),
	}

	est := cost.NewSpecEstimator(roots, cost.WithBudget(500))

	// Spec validation
	est.AddValidation(env, "spec.validations[0]", "1 + 1 == 2")

	// Resource entry
	r := est.Resource("res1")
	r.AddWhen(env, "spec.resources[res1].includeWhen", "true")
	r.AddExpr(env, "spec.resources[res1].name", "'app-' + 'main'")
	r.AddTemplate(env, "spec.resources[res1].template", &runtime.RawExtension{
		Raw: []byte(`{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"${'my-cfg'}"}}`),
	})

	res := est.Result()
	assert.Equal(t, cost.StateKnown, res.State)
	assert.Equal(t, cost.VerdictWithinBudget, res.Verdict)
	assert.Greater(t, res.TotalCost, uint64(0))
	assert.Greater(t, res.ValidationCost, uint64(0))
	require.Len(t, res.Resources, 1)
	assert.Equal(t, "res1", res.Resources[0].ID)
	assert.Equal(t, uint64(1), res.Resources[0].LoopBound)
	assert.Len(t, res.Resources[0].Expressions, 3) // when + name + template
}

func TestSpecEstimator_Looped(t *testing.T) {
	env, err := cel.NewEnv(template.BaseCELExtensions()...)
	require.NoError(t, err)

	roots := map[string]*apiservercel.DeclType{
		"item": apiservercel.StringType,
	}

	est := cost.NewSpecEstimator(roots, cost.WithBudget(1000))
	r := est.Resource("worker")

	forExpr := est.EstimateExpr(env, "spec.resources[worker].forEach", "'a,b,c'.split(',')")
	r.SetLoop(cost.LoopInfo{
		Bound:       10,
		CapExceeded: false,
		State:       cost.StateKnown,
		Expr:        &forExpr,
	})

	r.AddExpr(env, "spec.resources[worker].name", "1 + 2")

	res := est.Result()
	assert.Equal(t, cost.StateKnown, res.State)
	assert.Equal(t, cost.VerdictWithinBudget, res.Verdict)
	require.Len(t, res.Resources, 1)
	assert.Equal(t, uint64(10), res.Resources[0].LoopBound)
	assert.Equal(t, uint64(10), res.TotalForEachExpansions)
}
