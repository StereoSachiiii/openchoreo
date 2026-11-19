// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package cost

import (
	"fmt"

	celast "github.com/google/cel-go/common/ast"
	apiextschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"

	"github.com/openchoreo/openchoreo/internal/limits"
)

// MissingSchemaBound represents a detected OpenAPI schema limit missing from an author's spec.
type MissingSchemaBound struct {
	// FieldPath is the normalized property path, such as "parameters.tags" or "parameters.items[]".
	FieldPath string
	// MissingLimit is the type of limit missing from the schema ("maxItems", "maxLength", or "maxProperties").
	MissingLimit string
	// AssumedValue is the fallback ceiling assumed by the CEL cost estimator.
	AssumedValue uint64
	// YamlFieldPath is the exact path in spec YAML, such as "spec.parameters.openAPIV3Schema.properties.tags".
	YamlFieldPath string
	// Remediation is the actionable YAML snippet suggested to the user.
	Remediation string
}

// InspectMissingBounds walks the AST of an offending CEL expression and checks the OpenAPI schema
// for missing maxItems, maxLength, or maxProperties.
func InspectMissingBounds(
	expr celast.Expr,
	paramSchema, envConfigSchema *apiextschema.Structural,
) []MissingSchemaBound {
	if expr == nil {
		return nil
	}

	var missing []MissingSchemaBound
	seen := make(map[string]bool)

	// Collect full attribute paths like ["parameters", "items"] or ["parameters", "tags"]
	visitor := celast.NewExprVisitor(func(e celast.Expr) {
		path := extractPathFromExpr(e)
		if len(path) == 0 {
			return
		}
		rootName := path[0]
		var rootSchema *apiextschema.Structural
		var yamlPrefix string
		switch rootName {
		case "parameters":
			rootSchema = paramSchema
			yamlPrefix = "spec.parameters.openAPIV3Schema"
		case "environmentConfigs":
			rootSchema = envConfigSchema
			yamlPrefix = "spec.environmentConfigs.openAPIV3Schema"
		default:
			return
		}

		if rootSchema == nil {
			return
		}

		// Walk through path segments looking for schemas and check their bounds
		segments := path[1:]
		current := rootSchema
		yamlPath := yamlPrefix
		fieldPath := rootName

		for i, seg := range segments {
			if current == nil {
				break
			}
			if len(current.Properties) > 0 {
				if prop, ok := current.Properties[seg]; ok {
					current = &prop
					yamlPath += ".properties." + seg
					fieldPath += "." + seg
				} else {
					break
				}
			} else {
				break
			}

			// Check current node bounds
			hasMissingMaxItems := checkNodeBounds(current, fieldPath, yamlPath, &missing, seen)

			// If current is array, check its items schema as well.
			// However, if the array itself is already unbounded (missing maxItems), the O(N) array size
			// is the primary cause of budget failure. Avoid spamming child scalar bounds (maxLength)
			// unless the parent array is either already bounded by maxItems or explicitly demands items bounds.
			if current.Generic.Type == schemaTypeArray && current.Items != nil {
				if !hasMissingMaxItems {
					itemFieldPath := fieldPath + "[]"
					itemYamlPath := yamlPath + ".items"
					checkNodeBounds(current.Items, itemFieldPath, itemYamlPath, &missing, seen)
				}
			}

			_ = i
		}
	})

	celast.PostOrderVisit(expr, visitor)
	return missing
}

func checkNodeBounds(
	node *apiextschema.Structural,
	fieldPath, yamlPath string,
	missing *[]MissingSchemaBound,
	seen map[string]bool,
) bool {
	if node == nil {
		return false
	}

	missingFound := false
	bounds := limits.ActiveSchemaBounds()

	// 1. Array missing maxItems
	if node.Generic.Type == schemaTypeArray || node.Items != nil {
		if node.ValueValidation == nil || node.ValueValidation.MaxItems == nil {
			missingFound = true
			key := fieldPath + ":maxItems"
			if !seen[key] {
				seen[key] = true
				assumed := maxItemsInObject(minItemSize(node, nil))
				if bounds.MaxItems > 0 {
					assumed = uint64(bounds.MaxItems)
				}
				*missing = append(*missing, MissingSchemaBound{
					FieldPath:     fieldPath,
					MissingLimit:  "maxItems",
					AssumedValue:  assumed,
					YamlFieldPath: yamlPath,
					Remediation:   fmt.Sprintf("Add a limit under %s, for example:\n        maxItems: 50", yamlPath),
				})
			}
		}
	}

	// 2. String missing maxLength
	if node.Generic.Type == "string" {
		if node.ValueValidation == nil || (node.ValueValidation.MaxLength == nil && len(node.ValueValidation.Enum) == 0) {
			missingFound = true
			key := fieldPath + ":maxLength"
			if !seen[key] {
				seen[key] = true
				assumed := DefaultMaxLength
				if bounds.MaxLength > 0 {
					assumed = uint64(bounds.MaxLength)
				}
				*missing = append(*missing, MissingSchemaBound{
					FieldPath:     fieldPath,
					MissingLimit:  "maxLength",
					AssumedValue:  assumed,
					YamlFieldPath: yamlPath,
					Remediation:   fmt.Sprintf("Add a limit under %s, for example:\n        maxLength: 100", yamlPath),
				})
			}
		}
	}

	// 3. Object / Map missing maxProperties
	if node.Generic.Type == "object" && node.AdditionalProperties != nil {
		if node.ValueValidation == nil || node.ValueValidation.MaxProperties == nil {
			missingFound = true
			key := fieldPath + ":maxProperties"
			if !seen[key] {
				seen[key] = true
				assumed := DefaultMaxProperties
				if bounds.MaxProperties > 0 {
					assumed = uint64(bounds.MaxProperties)
				}
				*missing = append(*missing, MissingSchemaBound{
					FieldPath:     fieldPath,
					MissingLimit:  "maxProperties",
					AssumedValue:  assumed,
					YamlFieldPath: yamlPath,
					Remediation:   fmt.Sprintf("Add a limit under %s, for example:\n        maxProperties: 20", yamlPath),
				})
			}
		}
	}

	return missingFound
}

// extractPathFromExpr walks backwards from a Select node to its root Ident.
func extractPathFromExpr(e celast.Expr) []string {
	if e == nil {
		return nil
	}
	var path []string
	curr := e
	for {
		if curr.Kind() == celast.SelectKind {
			sel := curr.AsSelect()
			path = append([]string{sel.FieldName()}, path...)
			curr = sel.Operand()
		} else if curr.Kind() == celast.IdentKind {
			ident := curr.AsIdent()
			path = append([]string{ident}, path...)
			return path
		} else {
			return nil
		}
	}
}
