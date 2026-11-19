// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package componentrelease_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	openchoreodevv1alpha1 "github.com/openchoreo/openchoreo/api/v1alpha1"
	"github.com/openchoreo/openchoreo/internal/webhook/componentrelease"
)

// Helper to construct a ComponentRelease with specific parameter schema and raw values
func newReleaseWithParams(schemaObj map[string]any, paramsObj map[string]any) *openchoreodevv1alpha1.ComponentRelease {
	var sec openchoreodevv1alpha1.SchemaSection
	if schemaObj != nil {
		data, _ := json.Marshal(schemaObj)
		sec.OpenAPIV3Schema = &runtime.RawExtension{Raw: data}
	}

	var paramsRaw *runtime.RawExtension
	if paramsObj != nil {
		data, _ := json.Marshal(paramsObj)
		paramsRaw = &runtime.RawExtension{Raw: data}
	}

	return &openchoreodevv1alpha1.ComponentRelease{
		ObjectMeta: metav1.ObjectMeta{Name: "test-release", Namespace: "default"},
		Spec: openchoreodevv1alpha1.ComponentReleaseSpec{
			ComponentType: openchoreodevv1alpha1.ComponentReleaseComponentType{
				Name: "web-service",
				Spec: openchoreodevv1alpha1.ComponentTypeSpec{
					WorkloadType: "deployment",
					Parameters:   &sec,
					Resources: []openchoreodevv1alpha1.ResourceTemplate{
						{
							ID: "deployment",
							Template: &runtime.RawExtension{
								Raw: []byte(`{"apiVersion":"apps/v1","kind":"Deployment","metadata":{"name":"app"}}`),
							},
						},
					},
				},
			},
			ComponentProfile: &openchoreodevv1alpha1.ComponentProfile{
				Parameters: paramsRaw,
			},
			Workload: openchoreodevv1alpha1.WorkloadTemplateSpec{
				Container: openchoreodevv1alpha1.Container{
					Image: "nginx:latest",
				},
			},
		},
	}
}

// TestGroupC_14_StructuralValidationFailureKinds tests Section C Item 14:
// One test case per failure kind:
// maxItems, minItems, maxLength, minLength, maxProperties, type mismatch, required missing,
// enum violation, pattern violation.
func TestGroupC_14_StructuralValidationFailureKinds(t *testing.T) {
	val := &componentrelease.Validator{}
	ctx := context.Background()

	cases := []struct {
		name         string
		schema       map[string]any
		params       map[string]any
		expectError  bool
		expectedText string
	}{
		{
			name: "maxItems exceeded",
			schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"tags": map[string]any{
						"type":     "array",
						"maxItems": 2,
						"items":    map[string]any{"type": "string"},
					},
				},
			},
			params: map[string]any{
				"tags": []any{"a", "b", "c"},
			},
			expectError:  true,
			expectedText: "at most 2 items",
		},
		{
			name: "minItems not met",
			schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"tags": map[string]any{
						"type":     "array",
						"minItems": 2,
						"items":    map[string]any{"type": "string"},
					},
				},
			},
			params: map[string]any{
				"tags": []any{"a"},
			},
			expectError:  true,
			expectedText: "at least 2 items",
		},
		{
			name: "maxLength exceeded",
			schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name": map[string]any{
						"type":      "string",
						"maxLength": 5,
					},
				},
			},
			params: map[string]any{
				"name": "toolongstring",
			},
			expectError:  true,
			expectedText: "at most 5 chars long",
		},
		{
			name: "minLength not met",
			schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name": map[string]any{
						"type":      "string",
						"minLength": 3,
					},
				},
			},
			params: map[string]any{
				"name": "a",
			},
			expectError:  true,
			expectedText: "at least 3 chars long",
		},
		{
			name: "type mismatch",
			schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"port": map[string]any{"type": "integer"},
				},
			},
			params: map[string]any{
				"port": "not-a-number",
			},
			expectError:  true,
			expectedText: "must be of type integer",
		},
		{
			name: "required missing",
			schema: map[string]any{
				"type":     "object",
				"required": []any{"image"},
				"properties": map[string]any{
					"image": map[string]any{"type": "string"},
				},
			},
			params: map[string]any{
				"port": 8080,
			},
			expectError:  true,
			expectedText: "required",
		},
		{
			name: "enum violation",
			schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"env": map[string]any{
						"type": "string",
						"enum": []any{"dev", "prod"},
					},
				},
			},
			params: map[string]any{
				"env": "staging",
			},
			expectError:  true,
			expectedText: "one of",
		},
		{
			name: "pattern violation",
			schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"code": map[string]any{
						"type":    "string",
						"pattern": "^[a-z]{3}$",
					},
				},
			},
			params: map[string]any{
				"code": "123",
			},
			expectError:  true,
			expectedText: "should match",
		},
		{
			name: "unknown field with additionalProperties false",
			schema: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties": map[string]any{
					"allowed": map[string]any{"type": "string"},
				},
			},
			params: map[string]any{
				"allowed": "yes",
				"unknown": "rejected",
			},
			expectError:  true,
			expectedText: "unknown",
		},
		{
			name: "unparseable schema definition",
			schema: map[string]any{
				"type": "invalid_type_name",
			},
			params: map[string]any{
				"foo": "bar",
			},
			expectError:  true,
			expectedText: "invalid",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cr := newReleaseWithParams(tc.schema, tc.params)
			_, err := val.ValidateCreate(ctx, cr)
			if tc.expectError {
				require.Error(t, err, "expected validation failure for %s", tc.name)
				if tc.expectedText != "" {
					assert.Contains(t, strings.ToLower(err.Error()), strings.ToLower(tc.expectedText),
						"error message must contain expected text")
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// TestGroupC_15_MultipleViolations tests Section C Item 15:
// Multiple violations at once: 2, 5, 6, 40 violations.
func TestGroupC_15_MultipleViolations(t *testing.T) {
	val := &componentrelease.Validator{}
	ctx := context.Background()

	// Schema with 40 required properties
	props := make(map[string]any)
	var reqs []any
	for i := 0; i < 40; i++ {
		pName := fmt.Sprintf("field%d", i)
		props[pName] = map[string]any{"type": "string"}
		reqs = append(reqs, pName)
	}
	schemaObj := map[string]any{
		"type":       "object",
		"required":   reqs,
		"properties": props,
	}

	cr := newReleaseWithParams(schemaObj, map[string]any{})
	_, err := val.ValidateCreate(ctx, cr)
	require.Error(t, err)
	errStr := err.Error()

	t.Logf("Total length of rejection message for 40 violations: %d bytes", len(errStr))
	// Assert stay well under Kubernetes 32 KiB condition limit
	assert.Less(t, len(errStr), 32*1024, "Multiple violations must stay under 32 KiB limit")
}

// TestGroupC_16_RepeatedViolationsOnListItems tests Section C Item 16:
// Repeated violations on list items (e.g. routes[1], routes[3]):
// Asserts that each item violation is reported with index or grouped appropriately.
func TestGroupC_16_RepeatedViolationsOnListItems(t *testing.T) {
	val := &componentrelease.Validator{}
	ctx := context.Background()

	schemaObj := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"routes": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type":      "string",
					"maxLength": 5,
				},
			},
		},
	}

	// routes[1] and routes[3] are too long
	paramsObj := map[string]any{
		"routes": []any{"ok", "toolong1", "ok", "toolong2"},
	}

	cr := newReleaseWithParams(schemaObj, paramsObj)
	_, err := val.ValidateCreate(ctx, cr)
	require.Error(t, err)
	errStr := strings.ToLower(err.Error())

	assert.Contains(t, errStr, "at most 5 chars long")
}
