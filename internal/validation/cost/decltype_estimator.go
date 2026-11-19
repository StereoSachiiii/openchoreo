// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package cost

import (
	"strings"

	"github.com/google/cel-go/checker"
	"github.com/google/cel-go/common/types"
	apiextschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	apiservercel "k8s.io/apiserver/pkg/cel"

	"github.com/openchoreo/openchoreo/internal/limits"
)

// DeclTypeCostEstimator answers the two questions cel-go's static cost checker asks:
//
//   - EstimateSize:     how big can this collection or string be? (in this file)
//   - EstimateCallCost: how much does this function call cost? (in call_cost.go)
//
// Sizes come from the schema-derived DeclType tree (maxItems, maxLength, ...), so a template's
// cost is only as tight as the bounds its author declared in openAPIV3Schema.
type DeclTypeCostEstimator struct {
	// rootTypes maps each CEL variable (parameters, environmentConfigs, ...) to its type tree.
	rootTypes map[string]*apiservercel.DeclType
}

var _ checker.CostEstimator = &DeclTypeCostEstimator{}

// Size ceilings used when a schema declares no bound of its own.
const (
	// MaxObjectSizeBytes is the assumed ceiling on any one stored object: 1.5 MiB, etcd's default
	// request limit. Nothing a user submits can be larger, so it caps every collection and
	// string, whatever the schema says. It is deliberately tighter than the apiserver's 3 MiB
	// request limit, which the Kubernetes CEL library applies to unbounded fields.
	MaxObjectSizeBytes int64 = 3 * 1024 * 1024 / 2

	// DefaultMaxLength bounds a string: the object ceiling minus its enclosing "".
	DefaultMaxLength uint64 = uint64(MaxObjectSizeBytes - 2)

	// DefaultMaxProperties bounds a map/object: the object ceiling divided by the
	// smallest possible JSON entry (`"":"",` = 5 bytes).
	DefaultMaxProperties uint64 = uint64((MaxObjectSizeBytes - 2) / 5)

	// assumedStringLength is the size given to a platform string that has no declared maxLength
	// (a Kubernetes DNS-subdomain name, 253 chars).
	assumedStringLength uint64 = uint64(limits.MaxKubernetesNameLength)

	dnsLabelMaxLength uint64 = uint64(limits.MaxDNSLabelLength)
)

// NewDeclTypeCostEstimator creates a size and cost estimator backed by the registered DeclTypes.
func NewDeclTypeCostEstimator(roots map[string]*apiservercel.DeclType) *DeclTypeCostEstimator {
	return &DeclTypeCostEstimator{rootTypes: roots}
}

// WithRootType returns a copy of the estimator with one extra (or overridden) root type.
// It is used to bind a forEach loop variable.
func (e *DeclTypeCostEstimator) WithRootType(name string, t *apiservercel.DeclType) *DeclTypeCostEstimator {
	roots := make(map[string]*apiservercel.DeclType, len(e.rootTypes)+1)
	for k, v := range e.rootTypes {
		roots[k] = v
	}
	roots[name] = t
	return &DeclTypeCostEstimator{rootTypes: roots}
}

// EstimateSize returns the largest size the node at element.Path() can have, or nil if unknown.
//
// The path's first segment names a root variable, and the rest walks down into it, e.g.
// ["parameters", "routes"] is parameters.routes. Resolution runs in 4 clear steps:
//
//  1. Unpathed node (len(path) == 0): intermediate comprehension/function result.
//  2. resource.* root:                rendered output in post-render validations.
//  3. PlatformLimits table:           fixed bounds from embedded CRDs (e.g. workload.container.env).
//  4. DeclType schema walk:           author-declared maxItems/maxLength or KEP-2876 physical ceiling.
func (e *DeclTypeCostEstimator) EstimateSize(element checker.AstNode) *checker.SizeEstimate {
	if element == nil {
		return nil
	}

	path := element.Path()
	if len(path) == 0 {
		return estimateUnpathedNodeSize(element)
	}

	root := path[0]
	if root == "resource" {
		return estimateResourceNodeSize(element.Type())
	}

	rootType := e.rootTypes[root]
	if rootType == nil {
		return stringSizeOrNil(element.Type())
	}

	if limit, ok := PlatformLimits[strings.Join(path, ".")]; ok {
		if !limit.Bounded {
			return nil
		}
		return sizeUpTo(limit.Max)
	}

	return estimateDeclTypeNodeSize(root, walkDeclType(rootType, path[1:]), element.Type())
}

// estimateUnpathedNodeSize bounds intermediate AST expressions that have no variable path
// (for example, the output of a .filter() comprehension before .map() or .distinct()).
func estimateUnpathedNodeSize(element checker.AstNode) *checker.SizeEstimate {
	if cs := element.ComputedSize(); cs != nil {
		return cs
	}
	if size := stringSizeOrNil(element.Type()); size != nil {
		return size
	}
	if element.Type() == nil {
		return nil
	}

	switch element.Type().Kind() {
	case types.ListKind:
		if params := element.Type().Parameters(); len(params) > 0 {
			if strings.Contains(params[0].TypeName(), "EndpointResource") {
				return sizeUpTo(uint64(limits.MaxEndpointResourcesPerEp))
			}
		}
		if b := limits.ActiveSchemaBounds(); b.MaxItems > 0 {
			return sizeUpTo(uint64(b.MaxItems))
		}
		return sizeUpTo(uint64(limits.DefaultSchemaMaxItems))

	case types.MapKind:
		if b := limits.ActiveSchemaBounds(); b.MaxProperties > 0 {
			return sizeUpTo(uint64(b.MaxProperties))
		}
		return sizeUpTo(uint64(limits.DefaultSchemaMaxProperties))
	}
	return nil
}

// estimateResourceNodeSize bounds `resource.*` references in post-render validations,
// which inspect rendered Kubernetes objects rather than input schemas.
func estimateResourceNodeSize(t *types.Type) *checker.SizeEstimate {
	if size := stringSizeOrNil(t); size != nil {
		return size
	}
	if b := limits.ActiveSchemaBounds(); b.MaxItems > 0 {
		return sizeUpTo(uint64(b.MaxItems))
	}
	return sizeUpTo(maxItemsInObject(minItemSize(nil, t)))
}

// estimateDeclTypeNodeSize resolves the size of a schema-backed or reflected DeclType node:
//  1. If the node declares an explicit bound (node.MaxElements > 0), return min(declared, physicalCeiling).
//  2. If the node is a non-schema platform string (e.g. metadata.*), return 253 chars.
//  3. Otherwise, fall back to the KEP-2876 1.5 MiB physical payload ceiling.
func estimateDeclTypeNodeSize(root string, node *apiservercel.DeclType, elemType *types.Type) *checker.SizeEstimate {
	if node == nil {
		return nil
	}

	ceiling, hasCeiling := physicalMax(node)
	if node.MaxElements > 0 {
		declared := uint64(node.MaxElements)
		if hasCeiling {
			return sizeUpTo(min(declared, ceiling))
		}
		return sizeUpTo(declared)
	}

	isString := node == apiservercel.StringType || (node.CelType() != nil && node.CelType().Kind() == types.StringKind)
	if isString && root != "parameters" && root != "environmentConfigs" {
		return sizeUpTo(assumedStringLength)
	}

	if node == apiservercel.DynType {
		if size := stringSizeOrNil(elemType); size != nil {
			return size
		}
		if b := limits.ActiveSchemaBounds(); b.MaxItems > 0 {
			return sizeUpTo(uint64(b.MaxItems))
		}
	}

	if hasCeiling {
		return sizeUpTo(ceiling)
	}
	return nil
}

// walkDeclType follows path down from node and returns the type it lands on, or nil if a
// segment cannot be resolved. Segments are object field names, except for these synthetic ones:
//
//	@items, @values   element type of a list / map
//	@keys,  @indices  key type of a map / list
func walkDeclType(node *apiservercel.DeclType, path []string) *apiservercel.DeclType {
	for _, segment := range path {
		if node == nil {
			return nil
		}
		if node == apiservercel.DynType {
			return apiservercel.DynType
		}
		switch segment {
		case "@items", "@values":
			node = node.ElemType
		case "@keys", "@indices":
			node = node.KeyType
		default:
			field, ok := node.Fields[segment]
			if !ok || field.Type == nil {
				return nil
			}
			node = field.Type
		}
	}
	return node
}

// physicalMax is the most elements a collection or string of this type can hold within
// MaxObjectSizeBytes (1.5 MiB). Types that are not collections or strings have no such ceiling.
func physicalMax(node *apiservercel.DeclType) (uint64, bool) {
	celType := node.CelType()
	if celType == nil {
		return 0, false
	}
	switch celType.Kind() {
	case types.ListKind:
		return maxItemsInObject(minItemSize(nil, celType)), true
	case types.MapKind:
		return DefaultMaxProperties, true
	case types.StringKind:
		return DefaultMaxLength, true
	}
	return 0, false
}

func sizeUpTo(maxVal uint64) *checker.SizeEstimate {
	return &checker.SizeEstimate{Min: 0, Max: maxVal}
}

func stringSizeOrNil(t *types.Type) *checker.SizeEstimate {
	if t == types.StringType {
		return sizeUpTo(assumedStringLength)
	}
	return nil
}

// maxItemsInObject is how many elements of at least itemSize bytes fit in one 1.5 MiB object.
// Each element also costs one byte for its separating comma.
func maxItemsInObject(itemSize int64) uint64 {
	if itemSize <= 0 {
		itemSize = 1
	}
	items := (MaxObjectSizeBytes - 2) / (itemSize + 1) // the 2 is the enclosing []
	if items < 0 {
		return 0
	}
	return uint64(items)
}

// minItemSize is the smallest JSON encoding of one array element, in bytes.
func minItemSize(s *apiextschema.Structural, t *types.Type) int64 {
	if s != nil && s.Items != nil {
		if size, ok := minSizeOfSchemaType(s.Items); ok {
			return size
		}
	}
	if t != nil {
		if size, ok := minSizeOfCELElem(t); ok {
			return size
		}
	}
	return 1
}

func minSizeOfSchemaType(s *apiextschema.Structural) (int64, bool) {
	switch s.Generic.Type {
	case "string", schemaTypeArray:
		return 2, true
	case "integer", "number":
		return 1, true
	case "boolean":
		return 4, true
	case "object":
		size := int64(2)
		if s.ValueValidation != nil {
			for _, name := range s.ValueValidation.Required {
				size += int64(len(name)) + 4
			}
		}
		return size, true
	}
	return 0, false
}

func minSizeOfCELElem(list *types.Type) (int64, bool) {
	params := list.Parameters()
	if len(params) == 0 {
		return 0, false
	}
	switch params[0].Kind() {
	case types.StringKind, types.BytesKind, types.ListKind, types.MapKind, types.StructKind:
		return 2, true
	case types.IntKind, types.UintKind, types.DoubleKind:
		return 1, true
	case types.BoolKind:
		return 4, true
	}
	return 0, false
}
