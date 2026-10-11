// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package cost

import (
	apiextschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
)

// Default pessimistic sizing constants aligned with Kubernetes CRD CEL validation (KEP-2876).
// These bounds apply when the author omits explicit maxItems, maxLength, or maxProperties in their OpenAPI v3 schema.
const (
	// DefaultMaxItems is the assumed size for unbounded array/list types.
	DefaultMaxItems uint64 = 100

	// DefaultMaxLength is the assumed character length for unbounded string types.
	DefaultMaxLength uint64 = 1024

	// DefaultMaxProperties is the assumed entry count for unbounded object/map types.
	DefaultMaxProperties uint64 = 50

	// DefaultMaxNestedDepth prevents cyclic schema expansion explosion.
	DefaultMaxNestedDepth = 10
)

// SchemaLimits represents the resolved upper bounds for a schema property.
type SchemaLimits struct {
	// MaxItems specifies the maximum elements for an array/list.
	MaxItems uint64
	// MaxLength specifies the maximum character/byte length for a string.
	MaxLength uint64
	// MaxProperties specifies the maximum entries for a map/object.
	MaxProperties uint64
	// IsBounded indicates whether explicit schema bounds were provided by the author.
	IsBounded bool
}

// SchemaBoundsResolver traverses an OpenAPI v3 structural schema to inspect and resolve bounds.
type SchemaBoundsResolver struct {
	defaultMaxItems      uint64
	defaultMaxLength     uint64
	defaultMaxProperties uint64
}

// NewSchemaBoundsResolver creates a resolver with default or configured pessimistic fallbacks.
func NewSchemaBoundsResolver() *SchemaBoundsResolver {
	return &SchemaBoundsResolver{
		defaultMaxItems:      DefaultMaxItems,
		defaultMaxLength:     DefaultMaxLength,
		defaultMaxProperties: DefaultMaxProperties,
	}
}

// ResolveListBounds extracts maxItems from a structural schema node or applies the fallback.
func (r *SchemaBoundsResolver) ResolveListBounds(s *apiextschema.Structural) SchemaLimits {
	if s == nil {
		return SchemaLimits{MaxItems: r.defaultMaxItems, IsBounded: false}
	}
	if s.ValueValidation != nil && s.ValueValidation.MaxItems != nil {
		return SchemaLimits{
			MaxItems:  uint64(*s.ValueValidation.MaxItems),
			IsBounded: true,
		}
	}
	return SchemaLimits{MaxItems: r.defaultMaxItems, IsBounded: false}
}

// ResolveStringBounds extracts maxLength from a structural schema node or applies the fallback.
func (r *SchemaBoundsResolver) ResolveStringBounds(s *apiextschema.Structural) SchemaLimits {
	if s == nil {
		return SchemaLimits{MaxLength: r.defaultMaxLength, IsBounded: false}
	}
	if s.ValueValidation != nil && s.ValueValidation.MaxLength != nil {
		return SchemaLimits{
			MaxLength: uint64(*s.ValueValidation.MaxLength),
			IsBounded: true,
		}
	}
	return SchemaLimits{MaxLength: r.defaultMaxLength, IsBounded: false}
}

// ResolveMapBounds extracts maxProperties from a structural schema node or applies the fallback.
func (r *SchemaBoundsResolver) ResolveMapBounds(s *apiextschema.Structural) SchemaLimits {
	if s == nil {
		return SchemaLimits{MaxProperties: r.defaultMaxProperties, IsBounded: false}
	}
	if s.ValueValidation != nil && s.ValueValidation.MaxProperties != nil {
		return SchemaLimits{
			MaxProperties: uint64(*s.ValueValidation.MaxProperties),
			IsBounded:     true,
		}
	}
	return SchemaLimits{MaxProperties: r.defaultMaxProperties, IsBounded: false}
}

// FindFieldPath locates a nested structural schema node for a given dot-separated field path (e.g., "routes.items").
func (r *SchemaBoundsResolver) FindFieldPath(root *apiextschema.Structural, path []string) *apiextschema.Structural {
	current := root
	for _, segment := range path {
		if current == nil {
			return nil
		}
		if segment == "items" || segment == "[]" {
			current = current.Items
			continue
		}
		if len(current.Properties) > 0 {
			if prop, ok := current.Properties[segment]; ok {
				current = &prop
				continue
			}
		}
		if current.AdditionalProperties != nil {
			current = current.AdditionalProperties.Structural
			continue
		}
		return nil
	}
	return current
}
