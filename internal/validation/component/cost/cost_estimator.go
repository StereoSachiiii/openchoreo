// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package cost

import (
	"strings"

	"github.com/google/cel-go/checker"
	"github.com/google/cel-go/common/types"
	apiextschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
)

// StructuralCostEstimator implements cel-go's checker.CostEstimator interface using OpenAPI v3 structural schemas.
type StructuralCostEstimator struct {
	parametersSchema     *apiextschema.Structural
	envConfigsSchema     *apiextschema.Structural
	boundsResolver       *SchemaBoundsResolver
	defaultMaxItems      uint64
	defaultMaxLength     uint64
	defaultMaxProperties uint64
}

// NewStructuralCostEstimator creates a cost estimator backed by structural schemas for parameters & environmentConfigs.
func NewStructuralCostEstimator(parametersSchema, envConfigsSchema *apiextschema.Structural) *StructuralCostEstimator {
	return &StructuralCostEstimator{
		parametersSchema:     parametersSchema,
		envConfigsSchema:     envConfigsSchema,
		boundsResolver:       NewSchemaBoundsResolver(),
		defaultMaxItems:      DefaultMaxItems,
		defaultMaxLength:     DefaultMaxLength,
		defaultMaxProperties: DefaultMaxProperties,
	}
}

// EstimateSize estimates the upper bound size of a collection or string node in the CEL AST based on structural schema bounds.
func (e *StructuralCostEstimator) EstimateSize(element checker.AstNode) *checker.SizeEstimate {
	if element == nil {
		return nil
	}

	path := element.Path()
	if len(path) > 0 {
		var rootSchema *apiextschema.Structural
		switch path[0] {
		case "parameters":
			rootSchema = e.parametersSchema
		case "environmentConfigs":
			rootSchema = e.envConfigsSchema
		}

		if rootSchema != nil {
			nodeSchema := e.resolvePathToStructural(rootSchema, path[1:])
			if nodeSchema != nil {
				return e.sizeFromStructural(nodeSchema, element.Type())
			}
		}
	}

	// Fallback based on type kind
	if t := element.Type(); t != nil {
		switch t.Kind() {
		case types.ListKind:
			return &checker.SizeEstimate{Min: 0, Max: e.defaultMaxItems}
		case types.MapKind, types.StructKind:
			return &checker.SizeEstimate{Min: 0, Max: e.defaultMaxProperties}
		case types.StringKind, types.BytesKind:
			return &checker.SizeEstimate{Min: 0, Max: e.defaultMaxLength}
		}
	}

	return nil
}

// resolvePathToStructural walks the structural schema properties according to the AST node Path.
func (e *StructuralCostEstimator) resolvePathToStructural(root *apiextschema.Structural, segments []string) *apiextschema.Structural {
	current := root
	for _, segment := range segments {
		if current == nil {
			return nil
		}
		if segment == "@items" || segment == "[]" {
			current = current.Items
			continue
		}
		if segment == "@values" {
			if current.AdditionalProperties != nil {
				current = current.AdditionalProperties.Structural
			}
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

// sizeFromStructural checks explicit ValueValidation limits on the structural schema.
func (e *StructuralCostEstimator) sizeFromStructural(s *apiextschema.Structural, t *types.Type) *checker.SizeEstimate {
	if s == nil {
		return nil
	}

	if s.ValueValidation != nil {
		if s.ValueValidation.MaxItems != nil {
			return &checker.SizeEstimate{
				Min: 0,
				Max: uint64(*s.ValueValidation.MaxItems),
			}
		}
		if s.ValueValidation.MaxLength != nil {
			return &checker.SizeEstimate{
				Min: 0,
				Max: uint64(*s.ValueValidation.MaxLength),
			}
		}
		if s.ValueValidation.MaxProperties != nil {
			return &checker.SizeEstimate{
				Min: 0,
				Max: uint64(*s.ValueValidation.MaxProperties),
			}
		}
	}

	if t != nil {
		switch t.Kind() {
		case types.ListKind:
			return &checker.SizeEstimate{Min: 0, Max: e.defaultMaxItems}
		case types.MapKind, types.StructKind:
			return &checker.SizeEstimate{Min: 0, Max: e.defaultMaxProperties}
		case types.StringKind, types.BytesKind:
			return &checker.SizeEstimate{Min: 0, Max: e.defaultMaxLength}
		}
	}

	return nil
}

// EstimateCallCost calculates static cost for built-in and OpenChoreo custom functions (oc_*).
func (e *StructuralCostEstimator) EstimateCallCost(function, overloadID string, target *checker.AstNode, args []checker.AstNode) *checker.CallEstimate {
	switch function {
	case "oc_hash":
		maxLen := e.defaultMaxLength
		if len(args) > 0 && args[0].ComputedSize() != nil {
			maxLen = args[0].ComputedSize().Max
		}
		return &checker.CallEstimate{
			CostEstimate: checker.CostEstimate{
				Min: 1,
				Max: 1 + uint64(float64(maxLen)*0.1),
			},
		}

	case "oc_generate_name", "oc_dns_label":
		maxLen := e.defaultMaxLength
		if len(args) > 0 && args[0].ComputedSize() != nil {
			maxLen = args[0].ComputedSize().Max
		}
		return &checker.CallEstimate{
			CostEstimate: checker.CostEstimate{
				Min: 1,
				Max: 1 + maxLen + uint64(float64(maxLen)*0.1),
			},
		}

	case "oc_merge":
		maxProps := e.defaultMaxProperties * 2
		return &checker.CallEstimate{
			CostEstimate: checker.CostEstimate{
				Min: 1,
				Max: 1 + maxProps,
			},
		}

	case "oc_omit":
		return &checker.CallEstimate{
			CostEstimate: checker.CostEstimate{
				Min: 1,
				Max: 1,
			},
		}
	}

	if strings.HasPrefix(function, "oc_") {
		return &checker.CallEstimate{
			CostEstimate: checker.CostEstimate{
				Min: 1,
				Max: 10,
			},
		}
	}

	return nil
}

var _ checker.CostEstimator = &StructuralCostEstimator{}
