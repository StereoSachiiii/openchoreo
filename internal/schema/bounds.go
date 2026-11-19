// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"fmt"
	"sort"

	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiextschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"

	"github.com/openchoreo/openchoreo/api/v1alpha1"
	"github.com/openchoreo/openchoreo/internal/limits"
)

// AppliedDefaultBound records a single default bound injected into an OpenAPI schema.
type AppliedDefaultBound struct {
	Path    string // e.g. "properties.tags" or "properties.tags.items"
	Keyword string // "maxItems", "maxProperties", or "maxLength"
	Value   int64
}

// WarningMessage formats an informational message for a field that received a platform default bound.
func (a AppliedDefaultBound) WarningMessage(sectionPrefix string) string {
	fullPath := sectionPrefix
	if a.Path != "" {
		fullPath = sectionPrefix + "." + a.Path
	}
	return fmt.Sprintf(
		"%s: no %s declared; platform default %s=%d is enforced on inputs (declare %s in openAPIV3Schema to override)",
		fullPath, a.Keyword, a.Keyword, a.Value, a.Keyword,
	)
}

// ApplyDefaultBoundsToJSONSchema walks a v1 JSONSchemaProps tree in place and populates
// default MaxItems, MaxProperties, and MaxLength wherever omitted by the schema author.
// Explicit bounds set by the author are never overwritten.
func ApplyDefaultBoundsToJSONSchema(s *extv1.JSONSchemaProps, bounds limits.SchemaBounds) []AppliedDefaultBound {
	var applied []AppliedDefaultBound
	applyBoundsJSONSchemaRecursive(s, "", bounds, &applied)
	return applied
}

func applyBoundsJSONSchemaRecursive(
	s *extv1.JSONSchemaProps,
	path string,
	bounds limits.SchemaBounds,
	applied *[]AppliedDefaultBound,
) {
	if s == nil {
		return
	}
	applyNodeBoundsJSONSchema(s, path, bounds, applied)
	applyBoundsJSONSchemaChildren(s, path, bounds, applied)
}

func applyNodeBoundsJSONSchema(
	s *extv1.JSONSchemaProps,
	path string,
	bounds limits.SchemaBounds,
	applied *[]AppliedDefaultBound,
) {
	// 1. Array missing maxItems (covers explicit type: "array", implicit arrays where items != nil, and tuple arrays)
	if (s.Type == typeArray || s.Items != nil) && s.MaxItems == nil && bounds.MaxItems > 0 {
		v := bounds.MaxItems
		if s.Items != nil && s.Items.Schema == nil && len(s.Items.JSONSchemas) > 0 {
			v = int64(len(s.Items.JSONSchemas))
		}
		s.MaxItems = &v
		*applied = append(*applied, AppliedDefaultBound{Path: path, Keyword: "maxItems", Value: v})
	}

	// 2. Map / dictionary / freeform / hybrid object missing maxProperties
	if isMapOrFreeformJSONSchema(s) && s.MaxProperties == nil && bounds.MaxProperties > 0 {
		v := bounds.MaxProperties + int64(len(s.Properties))
		s.MaxProperties = &v
		*applied = append(*applied, AppliedDefaultBound{Path: path, Keyword: "maxProperties", Value: v})
	}

	// 3. String (including format: "byte") or x-kubernetes-int-or-string missing maxLength (skip strings bounded by enum)
	if (s.Type == typeString || s.XIntOrString) && s.MaxLength == nil && len(s.Enum) == 0 && bounds.MaxLength > 0 {
		v := bounds.MaxLength
		s.MaxLength = &v
		*applied = append(*applied, AppliedDefaultBound{Path: path, Keyword: "maxLength", Value: v})
	}

	// 4. Untyped {} schema node (type == "" with no properties/items/composition/int-or-string)
	if isUntypedAnyJSONSchema(s) {
		applyUntypedNodeBounds(s, bounds)
	}
}

func isMapOrFreeformJSONSchema(s *extv1.JSONSchemaProps) bool {
	if s.Type != typeObject {
		return false
	}
	return s.AdditionalProperties != nil ||
		(s.XPreserveUnknownFields != nil && *s.XPreserveUnknownFields) ||
		(len(s.Properties) == 0 && len(s.OneOf) == 0 && len(s.AnyOf) == 0 && len(s.AllOf) == 0)
}

func isUntypedAnyJSONSchema(s *extv1.JSONSchemaProps) bool {
	return s.Type == "" && !s.XIntOrString && s.Items == nil && len(s.Properties) == 0 &&
		s.AdditionalProperties == nil && len(s.OneOf) == 0 && len(s.AnyOf) == 0 && len(s.AllOf) == 0
}

func applyUntypedNodeBounds(s *extv1.JSONSchemaProps, bounds limits.SchemaBounds) {
	if s.MaxItems == nil && bounds.MaxItems > 0 {
		v := bounds.MaxItems
		s.MaxItems = &v
	}
	if s.MaxProperties == nil && bounds.MaxProperties > 0 {
		v := bounds.MaxProperties
		s.MaxProperties = &v
	}
	if s.MaxLength == nil && len(s.Enum) == 0 && bounds.MaxLength > 0 {
		v := bounds.MaxLength
		s.MaxLength = &v
	}
}

func applyBoundsJSONSchemaChildren(
	s *extv1.JSONSchemaProps,
	path string,
	bounds limits.SchemaBounds,
	applied *[]AppliedDefaultBound,
) {
	if len(s.Properties) > 0 {
		keys := make([]string, 0, len(s.Properties))
		for k := range s.Properties {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			prop := s.Properties[k]
			childPath := joinSchemaPath(path, "properties."+k)
			applyBoundsJSONSchemaRecursive(&prop, childPath, bounds, applied)
			s.Properties[k] = prop
		}
	}

	if s.Items != nil {
		if s.Items.Schema != nil {
			applyBoundsJSONSchemaRecursive(s.Items.Schema, joinSchemaPath(path, "items"), bounds, applied)
		}
		for i := range s.Items.JSONSchemas {
			applyBoundsJSONSchemaRecursive(&s.Items.JSONSchemas[i], joinSchemaPath(path, fmt.Sprintf("items[%d]", i)), bounds, applied)
		}
	}

	if s.AdditionalProperties != nil && s.AdditionalProperties.Schema != nil {
		applyBoundsJSONSchemaRecursive(s.AdditionalProperties.Schema, joinSchemaPath(path, "additionalProperties"), bounds, applied)
	}

	for i := range s.OneOf {
		applyBoundsJSONSchemaRecursive(&s.OneOf[i], joinSchemaPath(path, fmt.Sprintf("oneOf[%d]", i)), bounds, applied)
	}
	for i := range s.AnyOf {
		applyBoundsJSONSchemaRecursive(&s.AnyOf[i], joinSchemaPath(path, fmt.Sprintf("anyOf[%d]", i)), bounds, applied)
	}
	for i := range s.AllOf {
		applyBoundsJSONSchemaRecursive(&s.AllOf[i], joinSchemaPath(path, fmt.Sprintf("allOf[%d]", i)), bounds, applied)
	}
}

// InspectDefaultBoundsOnStructural walks a Structural schema and returns the list of default bounds
// that apply to it. Does NOT mutate the Structural schema.
func InspectDefaultBoundsOnStructural(s *apiextschema.Structural, bounds limits.SchemaBounds) []AppliedDefaultBound {
	var applied []AppliedDefaultBound
	inspectStructuralRecursive(s, "", bounds, &applied)
	return applied
}

func inspectStructuralRecursive(
	s *apiextschema.Structural,
	path string,
	bounds limits.SchemaBounds,
	applied *[]AppliedDefaultBound,
) {
	if s == nil {
		return
	}
	vv := s.ValueValidation

	if (s.Type == typeArray || s.Items != nil) && (vv == nil || vv.MaxItems == nil) && bounds.MaxItems > 0 {
		*applied = append(*applied, AppliedDefaultBound{Path: path, Keyword: "maxItems", Value: bounds.MaxItems})
	}
	if s.Type == typeObject && len(s.Properties) == 0 && s.AdditionalProperties != nil && (vv == nil || vv.MaxProperties == nil) && bounds.MaxProperties > 0 {
		*applied = append(*applied, AppliedDefaultBound{Path: path, Keyword: "maxProperties", Value: bounds.MaxProperties})
	}
	if s.Type == typeString && (vv == nil || (vv.MaxLength == nil && len(vv.Enum) == 0)) && bounds.MaxLength > 0 {
		*applied = append(*applied, AppliedDefaultBound{Path: path, Keyword: "maxLength", Value: bounds.MaxLength})
	}

	if len(s.Properties) > 0 {
		keys := make([]string, 0, len(s.Properties))
		for k := range s.Properties {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			prop := s.Properties[k]
			inspectStructuralRecursive(&prop, joinSchemaPath(path, "properties."+k), bounds, applied)
		}
	}
	if s.Items != nil {
		inspectStructuralRecursive(s.Items, joinSchemaPath(path, "items"), bounds, applied)
	}
	if s.AdditionalProperties != nil && s.AdditionalProperties.Structural != nil {
		inspectStructuralRecursive(s.AdditionalProperties.Structural, joinSchemaPath(path, "additionalProperties"), bounds, applied)
	}
}

// SectionToJSONSchemaUnbounded converts a SchemaSection to JSON Schema WITHOUT injecting platform default bounds.
// Used by scaffolding generators that emit user-facing YAML comments from the author's original schema.
func SectionToJSONSchemaUnbounded(section *v1alpha1.SchemaSection) (*extv1.JSONSchemaProps, error) {
	fields, err := unmarshalSectionSchema(section)
	if err != nil {
		return nil, err
	}
	if fields == nil {
		return &extv1.JSONSchemaProps{
			Type:       "object",
			Properties: map[string]extv1.JSONSchemaProps{},
		}, nil
	}
	return openAPIV3ToJSONSchemaInternal(fields, false)
}

func joinSchemaPath(prefix, segment string) string {
	if prefix == "" {
		return segment
	}
	return prefix + "." + segment
}
