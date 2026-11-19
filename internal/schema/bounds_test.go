// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/openchoreo/openchoreo/internal/limits"
)

func TestApplyDefaultBoundsToJSONSchema(t *testing.T) {
	limits.SetActiveSchemaBounds(limits.OptInSchemaBounds())
	defer limits.SetActiveSchemaBounds(limits.DefaultSchemaBounds())

	t.Run("array without maxItems gets DefaultSchemaMaxItems and rejects N+1 items", func(t *testing.T) {
		raw := map[string]any{
			"type": "object",
			"properties": map[string]any{
				"tags": map[string]any{
					"type":  "array",
					"items": map[string]any{"type": "string"},
				},
			},
		}
		v1Schema, err := OpenAPIV3ToJSONSchema(raw)
		require.NoError(t, err)
		require.NotNil(t, v1Schema.Properties["tags"].MaxItems)
		assert.Equal(t, limits.DefaultSchemaMaxItems, *v1Schema.Properties["tags"].MaxItems)
		require.NotNil(t, v1Schema.Properties["tags"].Items.Schema.MaxLength)
		assert.Equal(t, limits.DefaultSchemaMaxLength, *v1Schema.Properties["tags"].Items.Schema.MaxLength)

		// Validate with tight test bounds
		limits.SetActiveSchemaBounds(limits.SchemaBounds{
			MaxItems:        3,
			MaxProperties:   3,
			MaxLength:       10,
			MaxMapKeyLength: 253,
		})
		defer limits.SetActiveSchemaBounds(limits.OptInSchemaBounds())

		boundedSchema, err := OpenAPIV3ToJSONSchema(raw)
		require.NoError(t, err)
		assert.NoError(t, ValidateWithJSONSchema(map[string]any{"tags": []any{"a", "b", "c"}}, boundedSchema))
		assert.Error(t, ValidateWithJSONSchema(map[string]any{"tags": []any{"a", "b", "c", "d"}}, boundedSchema))
	})

	t.Run("author explicit bounds are preserved", func(t *testing.T) {
		raw := map[string]any{
			"type": "object",
			"properties": map[string]any{
				"tags": map[string]any{
					"type":     "array",
					"maxItems": float64(5000),
					"items": map[string]any{
						"type":      "string",
						"maxLength": float64(128),
					},
				},
				"mode": map[string]any{
					"type": "string",
					"enum": []any{"fast", "slow"},
				},
			},
		}
		v1Schema, err := OpenAPIV3ToJSONSchema(raw)
		require.NoError(t, err)
		require.NotNil(t, v1Schema.Properties["tags"].MaxItems)
		assert.Equal(t, int64(5000), *v1Schema.Properties["tags"].MaxItems)
		require.NotNil(t, v1Schema.Properties["tags"].Items.Schema.MaxLength)
		assert.Equal(t, int64(128), *v1Schema.Properties["tags"].Items.Schema.MaxLength)
		// Enum string should not get MaxLength injected
		assert.Nil(t, v1Schema.Properties["mode"].MaxLength)
	})

	t.Run("map and hybrid object get maxProperties while fixed-property object does not", func(t *testing.T) {
		raw := map[string]any{
			"type": "object",
			"properties": map[string]any{
				"labels": map[string]any{
					"type":                 "object",
					"additionalProperties": map[string]any{"type": "string"},
				},
				"hybrid": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"fixed1": map[string]any{"type": "string"},
						"fixed2": map[string]any{"type": "string"},
					},
					"additionalProperties": map[string]any{"type": "string"},
				},
				"fixed": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"host": map[string]any{"type": "string"},
					},
				},
			},
		}
		v1Schema, err := OpenAPIV3ToJSONSchema(raw)
		require.NoError(t, err)
		require.NotNil(t, v1Schema.Properties["labels"].MaxProperties)
		assert.Equal(t, limits.DefaultSchemaMaxProperties, *v1Schema.Properties["labels"].MaxProperties)

		require.NotNil(t, v1Schema.Properties["hybrid"].MaxProperties)
		assert.Equal(t, limits.DefaultSchemaMaxProperties+2, *v1Schema.Properties["hybrid"].MaxProperties)

		assert.Nil(t, v1Schema.Properties["fixed"].MaxProperties)
		assert.Equal(t, fmt.Sprintf("%d", limits.DefaultSchemaMaxLength), fmt.Sprintf("%d", *v1Schema.Properties["fixed"].Properties["host"].MaxLength))
	})

	t.Run("string exceeding DefaultSchemaMaxLength is rejected", func(t *testing.T) {
		raw := map[string]any{
			"type": "object",
			"properties": map[string]any{
				"bio": map[string]any{"type": "string"},
			},
		}
		v1Schema, err := OpenAPIV3ToJSONSchema(raw)
		require.NoError(t, err)
		assert.NoError(t, ValidateWithJSONSchema(map[string]any{"bio": strings.Repeat("a", int(limits.DefaultSchemaMaxLength))}, v1Schema))
		assert.Error(t, ValidateWithJSONSchema(map[string]any{"bio": strings.Repeat("a", int(limits.DefaultSchemaMaxLength)+1)}, v1Schema))
	})

	t.Run("InspectDefaultBoundsOnStructural identifies missing bounds without mutating", func(t *testing.T) {
		raw := map[string]any{
			"type": "object",
			"properties": map[string]any{
				"unboundedArray": map[string]any{
					"type":  "array",
					"items": map[string]any{"type": "string"},
				},
				"boundedArray": map[string]any{
					"type":     "array",
					"maxItems": float64(50),
					"items": map[string]any{
						"type":      "string",
						"maxLength": float64(100),
					},
				},
				"unboundedMap": map[string]any{
					"type":                 "object",
					"additionalProperties": map[string]any{"type": "string"},
				},
			},
		}

		structural, err := OpenAPIV3ToStructural(raw)
		require.NoError(t, err)
		require.NotNil(t, structural)

		bounds := limits.OptInSchemaBounds()
		applied := InspectDefaultBoundsOnStructural(structural, bounds)
		require.NotEmpty(t, applied)

		// Expect unboundedArray (maxItems) and unboundedArray.items (maxLength)
		// and unboundedMap (maxProperties) and unboundedMap.additionalProperties (maxLength)
		paths := make([]string, 0, len(applied))
		for _, b := range applied {
			paths = append(paths, b.Path+":"+b.Keyword)
			msg := b.WarningMessage("spec.parameters")
			assert.Contains(t, msg, "spec.parameters")
			assert.Contains(t, msg, b.Keyword)
		}
		assert.Contains(t, paths, "properties.unboundedArray:maxItems")
		assert.Contains(t, paths, "properties.unboundedArray.items:maxLength")
		assert.Contains(t, paths, "properties.unboundedMap:maxProperties")
	})
}
