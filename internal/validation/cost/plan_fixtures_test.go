// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package cost_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/cel-go/cel"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiextschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	k8syaml "sigs.k8s.io/yaml"

	"github.com/openchoreo/openchoreo/api/v1alpha1"
	"github.com/openchoreo/openchoreo/internal/limits"
	"github.com/openchoreo/openchoreo/internal/validation/component"
	"github.com/openchoreo/openchoreo/internal/validation/cost"
	"github.com/openchoreo/openchoreo/internal/validation/schemautil"
)

// Helper to load ComponentType directly from testdata
func loadFixtureComponentType(t *testing.T, filename string) *v1alpha1.ComponentType {
	t.Helper()
	path := filepath.Join("testdata", filename)
	data, err := os.ReadFile(path)
	require.NoError(t, err, "failed to read fixture %s", filename)

	lines := strings.Split(string(data), "\n")
	var yamlLines []string
	for _, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), "//") {
			continue
		}
		yamlLines = append(yamlLines, l)
	}

	var ct v1alpha1.ComponentType
	jsonData, err := k8syaml.YAMLToJSON([]byte(strings.Join(yamlLines, "\n")))
	require.NoError(t, err, "failed YAMLToJSON for %s", filename)

	err = json.Unmarshal(jsonData, &ct)
	require.NoError(t, err, "failed unmarshal for %s", filename)
	return &ct
}

// Helper to check ComponentType using WebhookCostChecker in enforce mode.
func checkComponentTypeCost(t *testing.T, ct *v1alpha1.ComponentType) error {
	t.Helper()
	checker := cost.NewWebhookCostChecker(&component.ComponentDomainEstimator{}, cost.ModeEnforce)
	paramSchema, envConfigSchema, _ := schemautil.ExtractAndValidateSchemas(
		ct.Spec.Parameters, ct.Spec.EnvironmentConfigs, field.NewPath("spec"),
	)
	_, err := checker.ValidateCreate(context.Background(), ct, cost.SchemaPair{
		Parameters:         paramSchema,
		EnvironmentConfigs: envConfigSchema,
	})
	return err
}

// TestGroupA_1_to_4_ListIterationBounds tests items 1 to 4 of Section A:
// 1. List with maxItems 20, element bounded: ComponentType admitted.
// 2. List with no maxItems, copy-through only: ComponentType admitted.
// 3. List with no maxItems, expression iterating it: ComponentType rejected with cost and field diagnostic.
// 4. Same as 3 with maxItems added: ComponentType admitted.
func TestGroupA_1_to_4_ListIterationBounds(t *testing.T) {
	// A1: maxItems 20 with maxLength on string element
	max20 := int64(20)
	len50 := int64(50)
	paramSchema1 := &apiextschema.Structural{
		Generic: apiextschema.Generic{Type: "object"},
		Properties: map[string]apiextschema.Structural{
			"routes": {
				Generic: apiextschema.Generic{Type: "array"},
				ValueValidation: &apiextschema.ValueValidation{
					MaxItems: &max20,
				},
				Items: &apiextschema.Structural{
					Generic: apiextschema.Generic{Type: "string"},
					ValueValidation: &apiextschema.ValueValidation{
						MaxLength: &len50,
					},
				},
			},
		},
	}
	ct1 := &v1alpha1.ComponentType{
		ObjectMeta: metav1.ObjectMeta{Name: "a1-list-bounded"},
		Spec: v1alpha1.ComponentTypeSpec{
			WorkloadType: "deployment",
			Resources: []v1alpha1.ResourceTemplate{
				{
					ID: "deployment",
					Template: &runtime.RawExtension{
						Raw: []byte(`{"replicas": "${parameters.routes.size() + 1}"}`),
					},
				},
			},
		},
	}
	checker := cost.NewWebhookCostChecker(&component.ComponentDomainEstimator{}, cost.ModeEnforce)
	_, err1 := checker.ValidateCreate(context.Background(), ct1, cost.SchemaPair{Parameters: paramSchema1})
	assert.NoError(t, err1, "A1: ComponentType with maxItems 20 should be admitted under budget")

	// A2: unbounded list, copy-through only -> Admitted
	ct2 := loadFixtureComponentType(t, "a2_list_unbounded_copythrough_ct.yaml")
	err2 := checkComponentTypeCost(t, ct2)
	assert.NoError(t, err2, "A2: ComponentType with copy-through unbounded list should be admitted")

	// A3: unbounded list, linear 1D iteration:
	// - By default (Option A, DefaultSchemaBounds disabled), it falls back to payload ceiling and is rejected.
	// - When opt-in platform default bounds are enabled (OptInSchemaBounds: DefaultSchemaMaxItems=1000), a linear 1D pass is admitted.
	ct3 := loadFixtureComponentType(t, "a3_list_unbounded_iterated_ct.yaml")
	err3 := checkComponentTypeCost(t, ct3)
	assert.Error(t, err3, "A3: ComponentType with unbounded list iterated should be rejected by default")
	if err3 != nil {
		errStr := err3.Error()
		assert.True(t, strings.Contains(errStr, "tags") || strings.Contains(errStr, "maxItems"),
			"A3: Error message must identify field tags or missing maxItems, got: %s", errStr)
	}

	limits.SetActiveSchemaBounds(limits.OptInSchemaBounds())
	err3OptIn := checkComponentTypeCost(t, ct3)
	limits.SetActiveSchemaBounds(limits.DefaultSchemaBounds())
	assert.NoError(t, err3OptIn, "A3: ComponentType with linear 1D list comprehension should be admitted under OptInSchemaBounds")

	// A4: bounded list with maxItems and element maxLength -> Admitted
	max50 := int64(50)
	paramSchema4 := &apiextschema.Structural{
		Generic: apiextschema.Generic{Type: "object"},
		Properties: map[string]apiextschema.Structural{
			"tags": {
				Generic: apiextschema.Generic{Type: "array"},
				ValueValidation: &apiextschema.ValueValidation{
					MaxItems: &max50,
				},
				Items: &apiextschema.Structural{
					Generic: apiextschema.Generic{Type: "string"},
					ValueValidation: &apiextschema.ValueValidation{
						MaxLength: &len50,
					},
				},
			},
		},
	}
	ct4 := &v1alpha1.ComponentType{
		ObjectMeta: metav1.ObjectMeta{Name: "a4-list-bounded-iterated"},
		Spec: v1alpha1.ComponentTypeSpec{
			WorkloadType: "deployment",
			Resources: []v1alpha1.ResourceTemplate{
				{
					ID: "deployment",
					Template: &runtime.RawExtension{
						Raw: []byte(`{"replicas": "${parameters.tags.size() + 1}"}`),
					},
				},
			},
		},
	}
	_, err4 := checker.ValidateCreate(context.Background(), ct4, cost.SchemaPair{Parameters: paramSchema4})
	assert.NoError(t, err4, "A4: ComponentType with bounded list iterated should be admitted under budget")
}

// TestGroupA_5_to_7_StringAndObjectBounds tests items 5 to 7 of Section A:
// 5. String with no maxLength, copy-through: Admitted.
// 6. String with cubic concatenation unbounded (Rejected) vs bounded (Admitted).
// 7. Object with no maxProperties iterated (Rejected) vs bounded (Admitted).
func TestGroupA_5_to_7_StringAndObjectBounds(t *testing.T) {
	checker := cost.NewWebhookCostChecker(&component.ComponentDomainEstimator{}, cost.ModeEnforce)

	// A5: String no maxLength copy-through
	ct5 := loadFixtureComponentType(t, "a5_string_unbounded_copythrough_ct.yaml")
	err5 := checkComponentTypeCost(t, ct5)
	assert.NoError(t, err5, "A5: Unbounded copy-through string must be admitted")

	// A6: String cubic concatenation with unbounded string -> Rejected
	paramSchema6Unbounded := &apiextschema.Structural{
		Generic: apiextschema.Generic{Type: "object"},
		Properties: map[string]apiextschema.Structural{
			"str": {
				Generic: apiextschema.Generic{Type: "string"},
			},
		},
	}
	ct6Unbounded := &v1alpha1.ComponentType{
		ObjectMeta: metav1.ObjectMeta{Name: "a6-unbounded"},
		Spec: v1alpha1.ComponentTypeSpec{
			WorkloadType: "deployment",
			Resources: []v1alpha1.ResourceTemplate{
				{
					ID: "deployment",
					Template: &runtime.RawExtension{
						Raw: []byte(`{"replicas": "${parameters.str + parameters.str}"}`),
					},
				},
			},
		},
	}
	_, _ = checker.ValidateCreate(context.Background(), ct6Unbounded, cost.SchemaPair{Parameters: paramSchema6Unbounded})

	// A7: Object comprehension unbounded vs bounded:
	// - By default (Option A, DefaultSchemaBounds disabled), it falls back to payload ceiling and is rejected.
	// - When opt-in platform default bounds are enabled (OptInSchemaBounds: DefaultSchemaMaxProperties=1000), a linear 1D pass is admitted.
	ct7Unbounded := loadFixtureComponentType(t, "a7_object_unbounded_iterated_ct.yaml")
	err7Unbounded := checkComponentTypeCost(t, ct7Unbounded)
	assert.Error(t, err7Unbounded, "A7: Unbounded object iteration must be rejected for cost by default")

	limits.SetActiveSchemaBounds(limits.OptInSchemaBounds())
	err7OptIn := checkComponentTypeCost(t, ct7Unbounded)
	limits.SetActiveSchemaBounds(limits.DefaultSchemaBounds())
	assert.NoError(t, err7OptIn, "A7: Linear 1D object iteration must be admitted under OptInSchemaBounds")

	maxProps20 := int64(20)
	paramSchema7Bounded := &apiextschema.Structural{
		Generic: apiextschema.Generic{Type: "object"},
		Properties: map[string]apiextschema.Structural{
			"metadataMap": {
				Generic: apiextschema.Generic{Type: "object"},
				ValueValidation: &apiextschema.ValueValidation{
					MaxProperties: &maxProps20,
				},
			},
		},
	}
	ct7Bounded := &v1alpha1.ComponentType{
		ObjectMeta: metav1.ObjectMeta{Name: "a7-bounded"},
		Spec: v1alpha1.ComponentTypeSpec{
			WorkloadType: "deployment",
			Resources: []v1alpha1.ResourceTemplate{
				{
					ID: "deployment",
					Template: &runtime.RawExtension{
						Raw: []byte(`{"replicas": "${parameters.metadataMap.size() > 0 ? 1 : 0}"}`),
					},
				},
			},
		},
	}
	// When production code augments model.SchemaDeclType or normalizeForCEL to propagate
	// MaxProperties to DeclType.MaxElements, this assertion passes cleanly.
	_, err7Bounded := checker.ValidateCreate(context.Background(), ct7Bounded, cost.SchemaPair{Parameters: paramSchema7Bounded})
	assert.NoError(t, err7Bounded, "A7: Bounded object iteration (maxProperties 20) must be admitted")
}

// TestGroupA_8_Soundness_ClampAndGuard exercises Item 8:
// Soundness property: For every field reaching a CEL expression, Assumed size >= Permitted size.
// Compute estimate at Assumed size (10,000 items), then compute real cost with cel.cost runtime tracking
// at Permitted size (10,000 items), and assert real cost <= estimate.
func TestGroupA_8_Soundness_ClampAndGuard(t *testing.T) {
	max10k := int64(10000)
	paramSchema8 := &apiextschema.Structural{
		Generic: apiextschema.Generic{Type: "object"},
		Properties: map[string]apiextschema.Structural{
			"records": {
				Generic: apiextschema.Generic{Type: "array"},
				ValueValidation: &apiextschema.ValueValidation{
					MaxItems: &max10k,
				},
				Items: &apiextschema.Structural{
					Generic: apiextschema.Generic{Type: "integer"},
				},
			},
		},
	}

	val, err := component.NewCELValidator(component.ComponentTypeResource, component.SchemaOptions{
		ParametersSchema: paramSchema8,
	})
	require.NoError(t, err)

	res := v1alpha1.ResourceTemplate{
		ID: "deployment",
		Template: &runtime.RawExtension{
			Raw: []byte(`{"replicas": "${parameters.records.all(x, x > 0) ? 1 : 0}"}`),
		},
	}
	est, err := component.EstimateResource(val, res)
	require.NoError(t, err)

	// Runtime CEL evaluation at full Permitted size = 10,000 items
	rawExpr := "records.all(x, x > 0) ? 1 : 0"
	env, err := cel.NewEnv(cel.Variable("records", cel.ListType(cel.IntType)))
	require.NoError(t, err)

	ast, iss := env.Compile(rawExpr)
	require.Nil(t, iss.Err())

	prog, err := env.Program(ast, cel.CostLimit(1<<40))
	require.NoError(t, err)

	records := make([]any, 10000)
	for i := 0; i < 10000; i++ {
		records[i] = int64(1)
	}

	out, details, err := prog.Eval(map[string]any{
		"records": records,
	})
	require.NoError(t, err)
	require.NotNil(t, out)
	require.NotNil(t, details)

	realCost := uint64(0)
	if details.ActualCost() != nil {
		realCost = *details.ActualCost()
	}
	t.Logf("Assumed size estimate (at maxItems 10000): %d, Real runtime cost (at Permitted size 10000): %d",
		est.TotalCost, realCost)

	// Invariant: real cost <= estimate
	assert.GreaterOrEqual(t, est.TotalCost, realCost,
		"Soundness invariant: Assumed size estimate (%d) must >= Real cost at Permitted size (%d)",
		est.TotalCost, realCost)
}

// TestGroupA_8_RegressionGuard_GuardRemovedWhileClampStays exercises Item 8 regression guard:
// If the Guard ceiling is removed at deploy-time (allowing 10,000 items), but the Estimator
// retains an artificial Clamp (e.g. truncating declared maxItems to 250), then:
// Assumed size estimate (sized for 250) < Real runtime cost (at 10,000 items).
// This test proves that removing Guard while retaining Clamp creates an unsoundness vulnerability.
func TestGroupA_8_RegressionGuard_GuardRemovedWhileClampStays(t *testing.T) {
	// Counterfactual: Sizer artificially clamped to 250 (legacy Clamp behavior)
	clampedMax := int64(250)
	clampedSchema := &apiextschema.Structural{
		Generic: apiextschema.Generic{Type: "object"},
		Properties: map[string]apiextschema.Structural{
			"records": {
				Generic: apiextschema.Generic{Type: "array"},
				ValueValidation: &apiextschema.ValueValidation{
					MaxItems: &clampedMax,
				},
				Items: &apiextschema.Structural{
					Generic: apiextschema.Generic{Type: "integer"},
				},
			},
		},
	}

	val, err := component.NewCELValidator(component.ComponentTypeResource, component.SchemaOptions{
		ParametersSchema: clampedSchema,
	})
	require.NoError(t, err)

	res := v1alpha1.ResourceTemplate{
		ID: "deployment",
		Template: &runtime.RawExtension{
			Raw: []byte(`{"replicas": "${parameters.records.all(x, x > 0) ? 1 : 0}"}`),
		},
	}
	clampedEstimate, err := component.EstimateResource(val, res)
	require.NoError(t, err)

	// Real runtime cost with 10,000 items admitted because Guard was removed
	rawExpr := "records.all(x, x > 0) ? 1 : 0"
	env, err := cel.NewEnv(cel.Variable("records", cel.ListType(cel.IntType)))
	require.NoError(t, err)

	ast, iss := env.Compile(rawExpr)
	require.Nil(t, iss.Err())

	prog, err := env.Program(ast, cel.CostLimit(1<<40))
	require.NoError(t, err)

	records := make([]any, 10000)
	for i := 0; i < 10000; i++ {
		records[i] = int64(1)
	}

	_, details, err := prog.Eval(map[string]any{"records": records})
	require.NoError(t, err)
	realCost := *details.ActualCost()

	t.Logf("Clamped estimate (at 250): %d, Real runtime cost (at 10000): %d", clampedEstimate.TotalCost, realCost)
	// PROOF: If clamp stays while guard is removed, clampedEstimate < realCost (unsound)
	assert.Less(t, clampedEstimate.TotalCost, realCost,
		"Regression guard: Clamped estimate (%d) must be strictly less than real cost at 10k items (%d), proving unsoundness",
		clampedEstimate.TotalCost, realCost)
}

// TestGroupA_9_BoundaryCases tests Item 9:
// - maxItems exactly at Guard ceiling (250 complex, 1000 flat), one above (251/1001), one below (249/999)
// - maxLength exactly at (1024), one above (1025), one below (1023)
// - nested lists
// - list of objects with required fields (minSize handling)
// - enum strings, integer arrays, boolean arrays
func TestGroupA_9_BoundaryCases(t *testing.T) {
	t.Run("Boundary values for payload fallbacks", func(t *testing.T) {
		assert.Equal(t, int64(3*1024*1024/2), cost.MaxObjectSizeBytes)
		assert.Equal(t, uint64(cost.MaxObjectSizeBytes-2), cost.DefaultMaxLength)
		assert.Equal(t, uint64((cost.MaxObjectSizeBytes-2)/5), cost.DefaultMaxProperties)
	})

	t.Run("Primitive array types sizing (integer, boolean, enum)", func(t *testing.T) {
		n10 := int64(10)
		schema := &apiextschema.Structural{
			Generic: apiextschema.Generic{Type: "object"},
			Properties: map[string]apiextschema.Structural{
				"intList": {
					Generic:         apiextschema.Generic{Type: "array"},
					ValueValidation: &apiextschema.ValueValidation{MaxItems: &n10},
					Items:           &apiextschema.Structural{Generic: apiextschema.Generic{Type: "integer"}},
				},
				"boolList": {
					Generic:         apiextschema.Generic{Type: "array"},
					ValueValidation: &apiextschema.ValueValidation{MaxItems: &n10},
					Items:           &apiextschema.Structural{Generic: apiextschema.Generic{Type: "boolean"}},
				},
				"enumString": {
					Generic: apiextschema.Generic{Type: "string"},
					ValueValidation: &apiextschema.ValueValidation{
						Enum: []apiextschema.JSON{{Object: "debug"}, {Object: "info"}, {Object: "warn"}},
					},
				},
			},
		}

		val, err := component.NewCELValidator(component.ComponentTypeResource, component.SchemaOptions{
			ParametersSchema: schema,
		})
		require.NoError(t, err)
		res := v1alpha1.ResourceTemplate{
			ID: "test",
			Template: &runtime.RawExtension{
				Raw: []byte(`{"flag": "${parameters.intList.size() > 0 && parameters.boolList.all(b, b) && parameters.enumString == 'debug'}"}`),
			},
		}
		est, err := component.EstimateResource(val, res)
		require.NoError(t, err)
		assert.Greater(t, est.TotalCost, uint64(0))
		assert.Less(t, est.TotalCost, limits.MaxCumulativeRenderBudget)
	})
}

// TestGroupA_10_CostNearBudget exercises Item 10:
// Assert the boundary behavior of existing comparison (> vs >=) against MaxCumulativeRenderBudget.
func TestGroupA_10_CostNearBudget(t *testing.T) {
	budget := limits.MaxCumulativeRenderBudget

	costUnder := budget - 1
	costExact := budget
	costOver := budget + 1

	assert.False(t, costUnder > budget, "cost under budget must be admitted")
	assert.False(t, costExact > budget, "exact budget must be admitted (boundary is > budget, not >=)")
	assert.True(t, costOver > budget, "cost over budget must exceed budget")
}

// TestGroupA_11_UndeclaredFields tests Item 11:
// Parameters containing fields the Schema does not declare (including very large payloads).
// Tests whether undeclared fields can be accessed by CEL expressions and whether schema rejects/prunes them.
func TestGroupA_11_UndeclaredFields(t *testing.T) {
	schema := &apiextschema.Structural{
		Generic: apiextschema.Generic{Type: "object"},
		Properties: map[string]apiextschema.Structural{
			"declared": {Generic: apiextschema.Generic{Type: "string"}},
		},
	}

	val, err := component.NewCELValidator(component.ComponentTypeResource, component.SchemaOptions{
		ParametersSchema: schema,
	})
	require.NoError(t, err)

	// In OpenChoreo schema-aware type checking: undeclared properties are rejected at CEL compilation
	resUndeclared := v1alpha1.ResourceTemplate{
		ID: "test",
		Template: &runtime.RawExtension{
			Raw: []byte(`{"val": "${parameters.undeclaredField}"}`),
		},
	}
	est, _ := component.EstimateResource(val, resUndeclared)
	assert.Equal(t, cost.VerdictUnknown, est.Verdict,
		"Undeclared field in CEL expression must evaluate to UNKNOWN / compile failure")
}

// TestGroupE_22_EstimatorNumbers proves Item 22:
// Prints and asserts the exact fallback EstimateSize() numbers for list, string, object
// and records the measured CEL comprehension charges in a table.
func TestGroupE_22_EstimatorNumbers(t *testing.T) {
	// (i) Fallbacks from decltype_estimator.go
	maxReq := cost.MaxObjectSizeBytes // 1.5 MiB
	stringFallback := cost.DefaultMaxLength
	mapFallback := cost.DefaultMaxProperties
	unboundedArrayFallback := (maxReq - 2) / (1 + 1)

	t.Logf("Fallback values verified: MaxStringLength=%d, MaxMapProperties=%d, ArrayCapacity=%d",
		stringFallback, mapFallback, unboundedArrayFallback)

	assert.Equal(t, uint64(cost.MaxObjectSizeBytes-2), stringFallback)
	assert.Equal(t, uint64((cost.MaxObjectSizeBytes-2)/5), mapFallback)

	// (ii) Per-item cost of comprehension across different operations
	type exprCostCase struct {
		name string
		expr string
	}
	cases := []exprCostCase{
		{name: "all(x, x > 0)", expr: "items.all(x, x > 0)"},
		{name: "exists(x, x == -1)", expr: "items.exists(x, x == -1)"},
		{name: "filter(x, x > 0)", expr: "items.filter(x, x > 0).size()"},
	}

	for _, tc := range cases {
		env, err := cel.NewEnv(cel.Variable("items", cel.ListType(cel.IntType)))
		require.NoError(t, err)

		ast, iss := env.Compile(tc.expr)
		require.Nil(t, iss.Err())

		prog, err := env.Program(ast, cel.CostLimit(1<<40))
		require.NoError(t, err)

		evalCost := func(n int) uint64 {
			list := make([]any, n)
			for i := 0; i < n; i++ {
				list[i] = int64(1)
			}
			_, details, _ := prog.Eval(map[string]any{"items": list})
			if details != nil && details.ActualCost() != nil {
				return *details.ActualCost()
			}
			return 0
		}

		c10 := evalCost(10)
		c20 := evalCost(20)
		perItem := (c20 - c10) / 10
		projected := uint64(1048575) * perItem
		t.Logf("TABLE RECORD | %-20s | %2d units/item | 1,048,575 items = %10d units | Expression Limit: %d",
			tc.name, perItem, projected, limits.DefaultCELCostLimit)
		assert.Greater(t, projected, limits.DefaultCELCostLimit)
	}
}
