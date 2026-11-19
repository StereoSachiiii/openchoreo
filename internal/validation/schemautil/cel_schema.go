// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package schemautil

import (
	apiextschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	"k8s.io/apiextensions-apiserver/pkg/apiserver/schema/cel/model"
	apiservercel "k8s.io/apiserver/pkg/cel"

	"github.com/openchoreo/openchoreo/internal/limits"
	"github.com/openchoreo/openchoreo/internal/validation/decltype"
)

const (
	schemaTypeArray  = "array"
	schemaTypeObject = "object"
	schemaTypeString = "string"
)

// SchemaToDeclType converts a structural schema to an apiservercel.DeclType,
// applying OpenAPI composition bounds synthesis (oneOf/anyOf/allOf),
// object maxProperties map normalization, and structural bounds propagation.
// If schema is nil or unconvertible, it returns an empty ObjectType with typeName.
func SchemaToDeclType(schema *apiextschema.Structural, typeName string) *apiservercel.DeclType {
	if schema != nil {
		normalized := NormalizeForCEL(schema)
		if dt := model.SchemaDeclType(normalized, false); dt != nil {
			PropagateStructuralBounds(normalized, dt)
			return dt.MaybeAssignTypeName(typeName)
		}
	}
	return apiservercel.NewObjectType(typeName, map[string]*apiservercel.DeclField{})
}

// PropagateStructuralBounds walks the Structural schema and corresponding DeclType tree,
// propagating author-declared bounds (MaxProperties, MaxItems, MaxLength) or platform default
// bounds (limits.ActiveSchemaBounds) onto DeclType.MaxElements so upstream SchemaDeclType's
// 3 MiB request-size fallback is replaced by the platform's enforced schema bounds.
func PropagateStructuralBounds(s *apiextschema.Structural, dt *apiservercel.DeclType) {
	propagateStructuralBoundsWithConfig(s, dt, limits.ActiveSchemaBounds())
}

func propagateStructuralBoundsWithConfig(s *apiextschema.Structural, dt *apiservercel.DeclType, bounds limits.SchemaBounds) {
	if s == nil || dt == nil || dt == apiservercel.StringType || dt == apiservercel.BytesType {
		return
	}
	applyNodeStructuralBounds(s, dt, bounds)
	propagateChildStructuralBounds(s, dt, bounds)
}

func applyNodeStructuralBounds(s *apiextschema.Structural, dt *apiservercel.DeclType, bounds limits.SchemaBounds) {
	vv := s.ValueValidation

	// 1. List / Array bounds
	if dt.IsList() || s.Type == schemaTypeArray || s.Items != nil {
		if vv != nil && vv.MaxItems != nil {
			dt.MaxElements = *vv.MaxItems
		} else if bounds.MaxItems > 0 {
			dt.MaxElements = bounds.MaxItems
		}
	}

	// 2. Map / Freeform Object bounds
	if dt.IsMap() || (s.Type == schemaTypeObject && (s.AdditionalProperties != nil || len(s.Properties) == 0)) {
		if vv != nil && vv.MaxProperties != nil {
			dt.MaxElements = *vv.MaxProperties
		} else if bounds.MaxProperties > 0 && len(s.Properties) == 0 {
			dt.MaxElements = bounds.MaxProperties
		}
		if dt.KeyType != nil && dt.KeyType.MaxElements == 0 && bounds.MaxMapKeyLength > 0 {
			dt.KeyType = decltype.StringTypeWithLimit(bounds.MaxMapKeyLength)
		}
	}

	// 3. String / format: byte / x-kubernetes-int-or-string bounds
	if s.Type == schemaTypeString || (s.Extensions.XIntOrString && !hasCompositionValidation(vv)) {
		if vv != nil && vv.MaxLength != nil {
			dt.MaxElements = *vv.MaxLength
		} else if (vv == nil || len(vv.Enum) == 0) && bounds.MaxLength > 0 {
			dt.MaxElements = bounds.MaxLength
		}
	}

	// 4. Composition (oneOf / anyOf / allOf) dyn nodes
	if hasCompositionValidation(vv) {
		applyCompositionNodeBounds(s, dt, bounds)
	}
}

func applyCompositionNodeBounds(s *apiextschema.Structural, dt *apiservercel.DeclType, bounds limits.SchemaBounds) {
	vv := s.ValueValidation
	switch {
	case vv.MaxItems != nil:
		dt.MaxElements = *vv.MaxItems
	case vv.MaxProperties != nil:
		dt.MaxElements = *vv.MaxProperties
	case vv.MaxLength != nil:
		dt.MaxElements = *vv.MaxLength
	case bounds.MaxItems > 0:
		dt.MaxElements = bounds.MaxItems
	}
	// Ensure synthesized properties from oneOf/anyOf/allOf branches are attached to dt.Fields
	// so DeclTypeCostEstimator.walkDeclType can resolve paths like parameters.tierConfig.services.
	if len(s.Properties) > 0 && len(dt.Fields) == 0 {
		dt.Fields = make(map[string]*apiservercel.DeclField, len(s.Properties))
		for propName, propSchema := range s.Properties {
			propCopy := propSchema
			dt.Fields[propName] = apiservercel.NewDeclField(propName, SchemaToDeclType(&propCopy, propName), false, nil, nil)
		}
	}
}

func propagateChildStructuralBounds(s *apiextschema.Structural, dt *apiservercel.DeclType, bounds limits.SchemaBounds) {
	if len(s.Properties) > 0 && len(dt.Fields) > 0 {
		for propName, propSchema := range s.Properties {
			f, ok := dt.Fields[propName]
			if !ok || f == nil || f.Type == nil {
				continue
			}
			if bounded, replaced := applyDefaultStringBoundIfNeeded(&propSchema, f.Type, bounds); replaced {
				f.Type = bounded
				continue
			}
			propagateStructuralBoundsWithConfig(&propSchema, f.Type, bounds)
		}
	}

	if s.AdditionalProperties != nil && s.AdditionalProperties.Structural != nil && dt.ElemType != nil {
		if bounded, replaced := applyDefaultStringBoundIfNeeded(s.AdditionalProperties.Structural, dt.ElemType, bounds); replaced {
			dt.ElemType = bounded
		} else {
			propagateStructuralBoundsWithConfig(s.AdditionalProperties.Structural, dt.ElemType, bounds)
		}
	}

	if s.Items != nil && dt.ElemType != nil {
		if bounded, replaced := applyDefaultStringBoundIfNeeded(s.Items, dt.ElemType, bounds); replaced {
			dt.ElemType = bounded
		} else {
			propagateStructuralBoundsWithConfig(s.Items, dt.ElemType, bounds)
		}
	}
}

func applyDefaultStringBoundIfNeeded(
	s *apiextschema.Structural,
	dt *apiservercel.DeclType,
	bounds limits.SchemaBounds,
) (*apiservercel.DeclType, bool) {
	if (dt != apiservercel.StringType && dt != apiservercel.BytesType) || s.Type != schemaTypeString {
		return dt, false
	}
	hasDeclared := s.ValueValidation != nil && (s.ValueValidation.MaxLength != nil || len(s.ValueValidation.Enum) > 0)
	if !hasDeclared && bounds.MaxLength > 0 {
		return cloneWithLimit(dt, bounds.MaxLength), true
	}
	return dt, true
}

func cloneWithLimit(base *apiservercel.DeclType, limit int64) *apiservercel.DeclType {
	cp := *base
	cp.MaxElements = limit
	return &cp
}

// NormalizeForCEL returns a shallow-cloned structural schema normalized so every OpenAPI v3
// construct (implicit arrays, additionalProperties: true, freeform objects, untyped {} schemas,
// and oneOf/anyOf/allOf at root or with nested properties/items/additionalProperties) converts
// cleanly to a bounded CEL DeclType.
func NormalizeForCEL(s *apiextschema.Structural) *apiextschema.Structural {
	return normalizeForCELWithBounds(s, limits.ActiveSchemaBounds())
}

func normalizeForCELWithBounds(s *apiextschema.Structural, bounds limits.SchemaBounds) *apiextschema.Structural {
	if s == nil {
		return nil
	}
	out := *s
	if out.ValueValidation != nil {
		vvCopy := *out.ValueValidation
		out.ValueValidation = &vvCopy
	}

	// 1. Implicit array (items set without type: "array")
	if out.Type == "" && out.Items != nil {
		out.Type = schemaTypeArray
	}

	// 2. OpenAPI composition (oneOf / anyOf / allOf) at any level (root or typed object/array)
	if hasCompositionValidation(out.ValueValidation) {
		if out.Type == "" {
			out.Extensions.XIntOrString = true
		}
		synthesizeCompositionBounds(&out, bounds)
	}

	// 3. Untyped {} schema (Type == "" with no composition, properties, items, or additionalProperties)
	if out.Type == "" && !out.Extensions.XIntOrString && !out.Extensions.XPreserveUnknownFields {
		out.Extensions.XPreserveUnknownFields = true
	}

	// 4. Boolean additionalProperties: true -> convert to Structural with XIntOrString: true so SchemaDeclType builds a MapType
	if out.AdditionalProperties != nil && out.AdditionalProperties.Bool && out.AdditionalProperties.Structural == nil {
		out.AdditionalProperties = &apiextschema.StructuralOrBool{
			Bool: true,
			Structural: &apiextschema.Structural{
				Extensions: apiextschema.Extensions{
					XIntOrString: true,
				},
			},
		}
	}

	// 5. Freeform object (type: "object" with no static properties and no additionalProperties)
	// When maxProperties is declared OR platform default MaxProperties > 0 is active (or XPreserveUnknownFields is set),
	// normalize into a MapType(string, dyn) so CEL map operations (.all(), .size(), key lookup) compile and are bounded.
	hasExplicitMaxProps := out.ValueValidation != nil && out.ValueValidation.MaxProperties != nil
	if out.Type == schemaTypeObject && len(out.Properties) == 0 && out.AdditionalProperties == nil &&
		(hasExplicitMaxProps || bounds.MaxProperties > 0 || out.Extensions.XPreserveUnknownFields) {
		out.AdditionalProperties = &apiextschema.StructuralOrBool{
			Structural: &apiextschema.Structural{
				Extensions: apiextschema.Extensions{
					XIntOrString: true,
				},
			},
		}
	}

	if out.Items != nil {
		out.Items = normalizeForCELWithBounds(out.Items, bounds)
	}

	if len(out.Properties) > 0 {
		props := make(map[string]apiextschema.Structural, len(out.Properties))
		for k, v := range out.Properties {
			normalized := normalizeForCELWithBounds(&v, bounds)
			props[k] = *normalized
		}
		out.Properties = props
	}

	if out.AdditionalProperties != nil && out.AdditionalProperties.Structural != nil {
		ap := *out.AdditionalProperties
		ap.Structural = normalizeForCELWithBounds(ap.Structural, bounds)
		out.AdditionalProperties = &ap
	}

	return &out
}

func hasCompositionValidation(v *apiextschema.ValueValidation) bool {
	return v != nil && (len(v.OneOf) > 0 || len(v.AnyOf) > 0 || len(v.AllOf) > 0)
}

type compositionBoundsAcc struct {
	bounds             limits.SchemaBounds
	maxItems           *int64
	maxLength          *int64
	maxProperties      *int64
	hasUnboundedItems  bool
	hasUnboundedLength bool
	hasUnboundedProps  bool
	mergedProps        map[string]apiextschema.Structural
}

func synthesizeCompositionBounds(s *apiextschema.Structural, bounds limits.SchemaBounds) {
	if s == nil || s.ValueValidation == nil {
		return
	}
	v := s.ValueValidation
	acc := compositionBoundsAcc{bounds: bounds}
	if len(s.Properties) > 0 {
		acc.mergedProps = make(map[string]apiextschema.Structural, len(s.Properties))
		for k, prop := range s.Properties {
			acc.mergedProps[k] = prop
		}
	}

	for _, b := range v.OneOf {
		acc.mergeBranch(b)
	}
	for _, b := range v.AnyOf {
		acc.mergeBranch(b)
	}
	if len(v.AllOf) > 0 {
		acc.mergeAllOf(v.AllOf)
	}

	if v.MaxItems == nil && acc.maxItems != nil && !acc.hasUnboundedItems {
		v.MaxItems = acc.maxItems
	}
	if v.MaxLength == nil && acc.maxLength != nil && !acc.hasUnboundedLength {
		v.MaxLength = acc.maxLength
	}
	if v.MaxProperties == nil && acc.maxProperties != nil && !acc.hasUnboundedProps {
		v.MaxProperties = acc.maxProperties
	}
	if len(acc.mergedProps) > 0 {
		s.Properties = acc.mergedProps
	}
}

func (acc *compositionBoundsAcc) mergeBranch(b apiextschema.NestedValueValidation) {
	branchType := b.ForbiddenGenerics.Type
	acc.maxItems, acc.hasUnboundedItems = accumulateBranchBound(
		acc.maxItems, acc.hasUnboundedItems, b.MaxItems,
		branchType == schemaTypeArray || b.Items != nil, acc.bounds.MaxItems,
	)
	acc.maxLength, acc.hasUnboundedLength = accumulateBranchBound(
		acc.maxLength, acc.hasUnboundedLength, b.MaxLength,
		branchType == schemaTypeString && len(b.Enum) == 0, acc.bounds.MaxLength,
	)
	acc.maxProperties, acc.hasUnboundedProps = accumulateBranchBound(
		acc.maxProperties, acc.hasUnboundedProps, b.MaxProperties,
		branchType == schemaTypeObject && len(b.Properties) == 0, acc.bounds.MaxProperties,
	)
	acc.mergeBranchProperties(b.Properties)
}

// mergeAllOf intersects bounds across allOf branches: any explicitly declared bound constrains the
// intersection, so the minimum declared bound wins (and is not loosened by sibling branches that omit it).
func (acc *compositionBoundsAcc) mergeAllOf(branches []apiextschema.NestedValueValidation) {
	var (
		minItems, minLength, minProps       *int64
		hasArray, hasString, hasFreeformObj bool
	)
	for _, b := range branches {
		branchType := b.ForbiddenGenerics.Type
		if branchType == schemaTypeArray || b.Items != nil {
			hasArray = true
		}
		if branchType == schemaTypeString && len(b.Enum) == 0 {
			hasString = true
		}
		if branchType == schemaTypeObject && len(b.Properties) == 0 {
			hasFreeformObj = true
		}
		minItems = minDeclaredBound(minItems, b.MaxItems)
		minLength = minDeclaredBound(minLength, b.MaxLength)
		minProps = minDeclaredBound(minProps, b.MaxProperties)
		acc.mergeAllOfProperties(b.Properties)
	}

	acc.maxItems, acc.hasUnboundedItems = intersectBranchBound(
		acc.maxItems, acc.hasUnboundedItems, minItems, hasArray, acc.bounds.MaxItems,
	)
	acc.maxLength, acc.hasUnboundedLength = intersectBranchBound(
		acc.maxLength, acc.hasUnboundedLength, minLength, hasString, acc.bounds.MaxLength,
	)
	acc.maxProperties, acc.hasUnboundedProps = intersectBranchBound(
		acc.maxProperties, acc.hasUnboundedProps, minProps, hasFreeformObj, acc.bounds.MaxProperties,
	)
}

func minDeclaredBound(current, declared *int64) *int64 {
	if declared == nil {
		return current
	}
	if current == nil || *declared < *current {
		val := *declared
		return &val
	}
	return current
}

func intersectBranchBound(
	current *int64,
	unbounded bool,
	minDeclared *int64,
	applies bool,
	defaultBound int64,
) (*int64, bool) {
	if minDeclared != nil {
		if current == nil || *minDeclared < *current {
			val := *minDeclared
			current = &val
		}
		return current, false
	}
	if applies && current == nil {
		if defaultBound > 0 {
			val := defaultBound
			return &val, unbounded
		}
		return nil, true
	}
	return current, unbounded
}

func accumulateBranchBound(
	current *int64,
	unbounded bool,
	declared *int64,
	applies bool,
	defaultBound int64,
) (*int64, bool) {
	effective := declared
	if effective == nil && applies {
		if defaultBound > 0 {
			val := defaultBound
			effective = &val
		} else {
			unbounded = true
		}
	}
	if effective != nil && (current == nil || *effective > *current) {
		val := *effective
		current = &val
	}
	return current, unbounded
}

func (acc *compositionBoundsAcc) mergeBranchProperties(props map[string]apiextschema.NestedValueValidation) {
	for propName, nestedProp := range props {
		branchProp := nestedToStructural(nestedProp, acc.bounds)
		if acc.mergedProps == nil {
			acc.mergedProps = make(map[string]apiextschema.Structural)
		}
		if existing, exists := acc.mergedProps[propName]; exists {
			acc.mergedProps[propName] = mergeStructuralSupremum(existing, branchProp)
		} else {
			acc.mergedProps[propName] = branchProp
		}
	}
}

// mergeAllOfProperties intersects overlapping property schemas across allOf branches.
// Omitted bounds stay nil during intersection so a tighter explicit bound on one branch
// is never overwritten by a default bound from a sibling branch.
func (acc *compositionBoundsAcc) mergeAllOfProperties(props map[string]apiextschema.NestedValueValidation) {
	for propName, nestedProp := range props {
		branchProp := nestedToStructural(nestedProp, limits.SchemaBounds{})
		if acc.mergedProps == nil {
			acc.mergedProps = make(map[string]apiextschema.Structural)
		}
		if existing, exists := acc.mergedProps[propName]; exists {
			acc.mergedProps[propName] = mergeStructuralInfimum(existing, branchProp)
		} else {
			acc.mergedProps[propName] = branchProp
		}
	}
}

func mergeStructuralInfimum(a, b apiextschema.Structural) apiextschema.Structural {
	out := a
	if out.Type == "" {
		out.Type = b.Type
	}
	if a.ValueValidation != nil || b.ValueValidation != nil {
		var vv apiextschema.ValueValidation
		if a.ValueValidation != nil {
			vv = *a.ValueValidation
		}
		if b.ValueValidation != nil {
			vv.MaxItems = minDeclaredBound(vv.MaxItems, b.ValueValidation.MaxItems)
			vv.MaxLength = minDeclaredBound(vv.MaxLength, b.ValueValidation.MaxLength)
			vv.MaxProperties = minDeclaredBound(vv.MaxProperties, b.ValueValidation.MaxProperties)
		}
		out.ValueValidation = &vv
	}
	if a.Items != nil && b.Items != nil {
		merged := mergeStructuralInfimum(*a.Items, *b.Items)
		out.Items = &merged
	} else if out.Items == nil && b.Items != nil {
		cp := *b.Items
		out.Items = &cp
	}
	if len(b.Properties) > 0 {
		if out.Properties == nil {
			out.Properties = make(map[string]apiextschema.Structural, len(b.Properties))
		}
		for k, bp := range b.Properties {
			if ap, exists := out.Properties[k]; exists {
				out.Properties[k] = mergeStructuralInfimum(ap, bp)
			} else {
				out.Properties[k] = bp
			}
		}
	}
	return out
}

func nestedToStructural(n apiextschema.NestedValueValidation, bounds limits.SchemaBounds) apiextschema.Structural {
	vv := n.ValueValidation
	st := apiextschema.Structural{
		Generic:         n.ForbiddenGenerics,
		Extensions:      n.ForbiddenExtensions,
		ValueValidation: &vv,
	}
	if st.Type == "" && n.Items != nil {
		st.Type = schemaTypeArray
	}
	if st.Type == "" && len(n.Properties) > 0 {
		st.Type = schemaTypeObject
	}
	if (st.Type == schemaTypeArray || n.Items != nil) && st.ValueValidation.MaxItems == nil && bounds.MaxItems > 0 {
		val := bounds.MaxItems
		st.ValueValidation.MaxItems = &val
	}
	if st.Type == schemaTypeString && st.ValueValidation.MaxLength == nil && len(st.ValueValidation.Enum) == 0 && bounds.MaxLength > 0 {
		val := bounds.MaxLength
		st.ValueValidation.MaxLength = &val
	}
	if st.Type == schemaTypeObject && len(n.Properties) == 0 && st.ValueValidation.MaxProperties == nil && bounds.MaxProperties > 0 {
		val := bounds.MaxProperties
		st.ValueValidation.MaxProperties = &val
	}
	if n.Items != nil {
		itemSt := nestedToStructural(*n.Items, bounds)
		st.Items = &itemSt
	}
	if len(n.Properties) > 0 {
		st.Properties = make(map[string]apiextschema.Structural, len(n.Properties))
		for k, v := range n.Properties {
			st.Properties[k] = nestedToStructural(v, bounds)
		}
	}
	if n.AdditionalProperties != nil {
		apSt := nestedToStructural(*n.AdditionalProperties, bounds)
		st.AdditionalProperties = &apiextschema.StructuralOrBool{Structural: &apSt}
	}
	return st
}

func mergeStructuralSupremum(a, b apiextschema.Structural) apiextschema.Structural {
	out := a
	if a.ValueValidation != nil || b.ValueValidation != nil {
		var vv apiextschema.ValueValidation
		if a.ValueValidation != nil {
			vv = *a.ValueValidation
		}
		if b.ValueValidation != nil {
			vv.MaxItems = maxInt64Ptr(vv.MaxItems, b.ValueValidation.MaxItems)
			vv.MaxLength = maxInt64Ptr(vv.MaxLength, b.ValueValidation.MaxLength)
			vv.MaxProperties = maxInt64Ptr(vv.MaxProperties, b.ValueValidation.MaxProperties)
		}
		out.ValueValidation = &vv
	}
	return out
}

func maxInt64Ptr(a, b *int64) *int64 {
	if a == nil || b == nil {
		return nil
	}
	val := *a
	if *b > val {
		val = *b
	}
	return &val
}
