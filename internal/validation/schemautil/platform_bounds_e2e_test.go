// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package schemautil_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"

	"github.com/openchoreo/openchoreo/api/v1alpha1"
	"github.com/openchoreo/openchoreo/internal/limits"
	"github.com/openchoreo/openchoreo/internal/schema"
	"github.com/openchoreo/openchoreo/internal/validation/component"
	"github.com/openchoreo/openchoreo/internal/validation/cost"
	"github.com/openchoreo/openchoreo/internal/validation/endpoint"
	"github.com/openchoreo/openchoreo/internal/validation/resource"
	"github.com/openchoreo/openchoreo/internal/validation/schemautil"
	"github.com/openchoreo/openchoreo/internal/validation/workflow"
)

func TestPlatformBounds_AdmissionAndCostParity(t *testing.T) {
	limits.SetActiveSchemaBounds(limits.OptInSchemaBounds())
	defer limits.SetActiveSchemaBounds(limits.DefaultSchemaBounds())

	rawSchema := []byte(`{
		"type": "object",
		"properties": {
			"items": {
				"type": "array",
				"items": { "type": "string" }
			},
			"labels": {
				"type": "object",
				"additionalProperties": { "type": "string" }
			},
			"description": {
				"type": "string"
			},
			"customCapItems": {
				"type": "array",
				"maxItems": 20,
				"items": { "type": "string", "maxLength": 64 }
			},
			"raisedCapItems": {
				"type": "array",
				"maxItems": 2500,
				"items": { "type": "string", "maxLength": 16 }
			}
		}
	}`)

	section := &v1alpha1.SchemaSection{
		OpenAPIV3Schema: &runtime.RawExtension{Raw: rawSchema},
	}

	// 1. Resolve bundle for runtime/admission JSON Schema validation
	structural, jsonSchema, err := schema.ResolveSectionToBundle(section)
	require.NoError(t, err)
	require.NotNil(t, structural)
	require.NotNil(t, jsonSchema)

	// Verify admission validation accepts values within platform defaults
	validData := map[string]any{
		"items":       []any{"a", "b"},
		"labels":      map[string]any{"env": "prod"},
		"description": strings.Repeat("x", int(limits.DefaultSchemaMaxLength)),
	}
	require.NoError(t, schema.ValidateWithJSONSchema(validData, jsonSchema))

	// Verify admission validation rejects string > DefaultSchemaMaxLength (4096)
	tooLongStr := map[string]any{
		"description": strings.Repeat("x", int(limits.DefaultSchemaMaxLength)+1),
	}
	assert.Error(t, schema.ValidateWithJSONSchema(tooLongStr, jsonSchema))

	// Verify admission validation rejects array > DefaultSchemaMaxItems (1000)
	overItems := make([]any, limits.DefaultSchemaMaxItems+1)
	for i := range overItems {
		overItems[i] = "v"
	}
	assert.Error(t, schema.ValidateWithJSONSchema(map[string]any{"items": overItems}, jsonSchema))

	// Verify explicit author maxItems (2500 > 1000) is honored and not clamped to 1000
	raisedItems := make([]any, 1500)
	for i := range raisedItems {
		raisedItems[i] = "v"
	}
	assert.NoError(t, schema.ValidateWithJSONSchema(map[string]any{"raisedCapItems": raisedItems}, jsonSchema))

	// 2. Verify static CEL cost DeclType bounds match the exact same defaults
	paramSchema, _, errs := schemautil.ExtractAndValidateSchemas(section, nil, field.NewPath("spec"))
	require.Empty(t, errs)
	require.NotNil(t, paramSchema)

	decl := schemautil.SchemaToDeclType(paramSchema, "Parameters")
	require.NotNil(t, decl)

	assert.Equal(t, limits.DefaultSchemaMaxItems, decl.Fields["items"].Type.MaxElements)
	assert.Equal(t, limits.DefaultSchemaMaxLength, decl.Fields["items"].Type.ElemType.MaxElements)
	assert.Equal(t, limits.DefaultSchemaMaxProperties, decl.Fields["labels"].Type.MaxElements)
	assert.Equal(t, limits.DefaultMapKeyMaxLength, decl.Fields["labels"].Type.KeyType.MaxElements)
	assert.Equal(t, limits.DefaultSchemaMaxLength, decl.Fields["labels"].Type.ElemType.MaxElements)
	assert.Equal(t, limits.DefaultSchemaMaxLength, decl.Fields["description"].Type.MaxElements)
	assert.Equal(t, int64(20), decl.Fields["customCapItems"].Type.MaxElements)
	assert.Equal(t, int64(64), decl.Fields["customCapItems"].Type.ElemType.MaxElements)
	assert.Equal(t, int64(2500), decl.Fields["raisedCapItems"].Type.MaxElements)
	assert.Equal(t, int64(16), decl.Fields["raisedCapItems"].Type.ElemType.MaxElements)

	// 3. Verify 1D linear comprehension is admitted while 2D quadratic comprehension is rejected
	val, err := component.NewCELValidator(component.ComponentTypeResource, component.SchemaOptions{
		ParametersSchema: paramSchema,
	})
	require.NoError(t, err)
	linearEst := component.EstimateExpression(val, `parameters.items.map(x, x).size()`, val.GetBaseEnv(), "test.linear")
	assert.Equal(t, cost.StateKnown, linearEst.State)
	assert.Equal(t, cost.VerdictWithinBudget, linearEst.Verdict)

	quadEst := component.EstimateExpression(val, `parameters.items.map(x, parameters.items.filter(y, y == x)).size()`, val.GetBaseEnv(), "test.quad")
	assert.Equal(t, cost.StateKnown, quadEst.State)
	assert.Equal(t, cost.VerdictOverBudget, quadEst.Verdict)
}

func TestSharedTreeEstimator_SingleExpressionLimit(t *testing.T) {
	limits.SetActiveSchemaBounds(limits.OptInSchemaBounds())
	defer limits.SetActiveSchemaBounds(limits.DefaultSchemaBounds())

	// 1. Verify ResourceTypeCostAggregator and WorkflowCostAggregator enforce single-expression limit (2M)
	rawSchema := []byte(`{
		"type": "object",
		"properties": {
			"items": {
				"type": "array",
				"items": { "type": "string" }
			}
		}
	}`)
	section := &v1alpha1.SchemaSection{
		OpenAPIV3Schema: &runtime.RawExtension{Raw: rawSchema},
	}
	paramSchema, _, errs := schemautil.ExtractAndValidateSchemas(section, nil, field.NewPath("spec"))
	require.Empty(t, errs)

	// Single expression with cost ~3.14M (> 2M single-expression limit, but < 10M cumulative budget)
	// must be marked VerdictOverBudget by both ResourceTypeCostAggregator and WorkflowCostAggregator.
	singleOverExpr := `${parameters.items.map(x, parameters.items.map(y, y)).size() > 0}`

	rtEst, err := resource.EstimateResourceTypeSpec(v1alpha1.ResourceTypeSpec{
		Resources: []v1alpha1.ResourceTypeManifest{
			{
				ID:          "db",
				IncludeWhen: singleOverExpr,
				Template:    &runtime.RawExtension{Raw: []byte(`{"apiVersion":"v1","kind":"ConfigMap"}`)},
			},
		},
	}, paramSchema, nil)
	require.NoError(t, err)
	assert.Equal(t, cost.VerdictOverBudget, rtEst.Verdict, "ResourceType must reject single expression > 2M even when total < 10M")

	wfEst, err := workflow.EstimateWorkflowSpec(v1alpha1.WorkflowSpec{
		RunTemplate: &runtime.RawExtension{
			Raw: []byte(`{"apiVersion":"argoproj.io/v1alpha1","kind":"Workflow","metadata":{"name":"${metadata.workflowRunName}","namespace":"${metadata.namespace}","labels":{"store":"${workflowplane.secretStore}"}}}`),
		},
		Resources: []v1alpha1.WorkflowResource{
			{
				ID:          "secret",
				IncludeWhen: singleOverExpr,
				Template:    &runtime.RawExtension{Raw: []byte(`{"apiVersion":"v1","kind":"Secret"}`)},
			},
		},
	}, paramSchema)
	require.NoError(t, err)
	assert.Equal(t, cost.VerdictOverBudget, wfEst.Verdict, "Workflow must reject single expression > 2M even when total < 10M")

	eptEst, err := endpoint.EstimateEndpointTypeSpec(v1alpha1.EndpointTypeSpec{
		Validations: []v1alpha1.ValidationRule{
			{
				Message: "check host",
				Rule:    singleOverExpr,
			},
		},
		Resources: []v1alpha1.ResourceTemplate{
			{
				ID:       "route",
				Template: &runtime.RawExtension{Raw: []byte(`{"apiVersion":"gateway.networking.k8s.io/v1","kind":"HTTPRoute"}`)},
			},
		},
	}, paramSchema, nil)
	require.NoError(t, err)
	assert.Equal(t, cost.VerdictOverBudget, eptEst.Verdict, "EndpointType must reject single expression > 2M even when total < 10M")
}

func TestAllOfIntersectionTakesTightestBound(t *testing.T) {
	rawSchema := []byte(`{
		"type": "object",
		"properties": {
			"tags": {
				"allOf": [
					{"type": "array", "maxItems": 10, "items": {"type": "string"}},
					{"type": "array", "items": {"type": "string"}}
				]
			},
			"tight": {
				"allOf": [
					{"type": "array", "maxItems": 50, "items": {"type": "string"}},
					{"type": "array", "maxItems": 12, "items": {"type": "string"}}
				]
			},
			"nestedObj": {
				"allOf": [
					{"type": "object", "properties": {"items": {"type": "array", "items": {"type": "string"}}}},
					{"type": "object", "properties": {"items": {"type": "array", "maxItems": 20, "items": {"type": "string"}}}}
				]
			}
		}
	}`)
	section := &v1alpha1.SchemaSection{
		OpenAPIV3Schema: &runtime.RawExtension{Raw: rawSchema},
	}

	for _, tc := range []struct {
		name   string
		bounds limits.SchemaBounds
	}{
		{name: "default disabled bounds", bounds: limits.DefaultSchemaBounds()},
		{name: "opt-in platform bounds", bounds: limits.OptInSchemaBounds()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			limits.SetActiveSchemaBounds(tc.bounds)
			defer limits.SetActiveSchemaBounds(limits.DefaultSchemaBounds())

			structural, _, errs := schemautil.ExtractAndValidateSchemas(section, nil, field.NewPath("spec"))
			require.Empty(t, errs)

			dt := schemautil.SchemaToDeclType(structural, "Parameters")
			require.NotNil(t, dt)

			tagsField := dt.Fields["tags"]
			require.NotNil(t, tagsField)
			assert.Equal(t, int64(10), tagsField.Type.MaxElements, "allOf must preserve declared maxItems=10 even when sibling omits it")

			tightField := dt.Fields["tight"]
			require.NotNil(t, tightField)
			assert.Equal(t, int64(12), tightField.Type.MaxElements, "allOf must take minimum declared bound min(50, 12) = 12")

			nestedObj := dt.Fields["nestedObj"]
			require.NotNil(t, nestedObj)
			nestedItems := nestedObj.Type.Fields["items"]
			require.NotNil(t, nestedItems)
			assert.Equal(t, int64(20), nestedItems.Type.MaxElements, "allOf overlapping properties must intersect to tightest declared maxItems=20")
		})
	}
}
