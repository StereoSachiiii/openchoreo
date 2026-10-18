// Copyright 2025 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package decltype

import (
	"reflect"
	"strings"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/types"
	apiservercel "k8s.io/apiserver/pkg/cel"
)

const (
	maxListSize = 1000
	maxMapSize  = 1000

	// Default string limit for platform fields (Kubernetes name limit = 253)
	defaultStringLimit = 253
	// DNS label string limit for namespace/label fields (Kubernetes DNS label limit = 63)
	dnsLabelStringLimit = 63
)

// StringTypeWithLimit returns a string DeclType with the given max element bound.
func StringTypeWithLimit(maxChars int64) *apiservercel.DeclType {
	t := apiservercel.NewSimpleTypeWithMinSize("string", cel.StringType, types.String(""), apiservercel.MinStringSize)
	t.MaxElements = maxChars
	return t
}

// FromGoType converts a Go type to a CEL DeclType using reflection.
// This is used for fixed-structure types like MetadataContext, WorkloadData, etc.
func FromGoType(t reflect.Type) *apiservercel.DeclType {
	return typeToDecl(t, make(map[reflect.Type]*apiservercel.DeclType))
}

// fromGoType is an internal alias for FromGoType.
func fromGoType(t reflect.Type) *apiservercel.DeclType {
	return FromGoType(t)
}

func typeToDecl(t reflect.Type, seen map[reflect.Type]*apiservercel.DeclType) *apiservercel.DeclType {
	// Handle pointers
	if t.Kind() == reflect.Ptr {
		return typeToDecl(t.Elem(), seen)
	}

	// Handle cycles
	if existing, ok := seen[t]; ok {
		return existing
	}

	switch t.Kind() {
	case reflect.String:
		return apiservercel.StringType
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return apiservercel.IntType
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return apiservercel.UintType
	case reflect.Float32, reflect.Float64:
		return apiservercel.DoubleType
	case reflect.Bool:
		return apiservercel.BoolType

	case reflect.Slice:
		elemType := typeToDecl(t.Elem(), seen)
		sliceLimit := int64(maxListSize)
		switch t.Elem().Name() {
		case "EnvVar", "EnvConfiguration":
			sliceLimit = 1024
		case "FileVar", "ConfigFileListEntry", "SecretFileListEntry", "FileConfiguration":
			sliceLimit = 256
		case "EnvsByContainerEntry":
			sliceLimit = 1
		case "EndpointVisibility":
			sliceLimit = 4
		}
		return apiservercel.NewListType(elemType, sliceLimit)

	case reflect.Map:
		keyType := typeToDecl(t.Key(), seen)
		elemType := typeToDecl(t.Elem(), seen)
		mapLimit := int64(maxMapSize)
		if t.Elem().Name() == "WorkloadEndpoint" || t.Elem().Name() == "EndpointData" {
			mapLimit = 128
		}
		return apiservercel.NewMapType(keyType, elemType, mapLimit)

	case reflect.Struct:
		return structToDecl(t, seen)

	case reflect.Interface:
		// interface{} or any → DynType
		return apiservercel.DynType

	default:
		return apiservercel.DynType
	}
}

func structToDecl(t reflect.Type, seen map[reflect.Type]*apiservercel.DeclType) *apiservercel.DeclType {
	// Create placeholder to handle cycles
	placeholder := &apiservercel.DeclType{}
	seen[t] = placeholder

	fields := make(map[string]*apiservercel.DeclField)

	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)

		// Skip unexported fields
		if !field.IsExported() {
			continue
		}

		// Get JSON tag name
		jsonName := getJSONFieldName(field)
		if jsonName == "-" {
			continue
		}

		fieldType := typeToDecl(field.Type, seen)

		// Refine bounds for specific struct fields based on domain knowledge
		if field.Type.Kind() == reflect.String || (field.Type.Kind() == reflect.Ptr && field.Type.Elem().Kind() == reflect.String) {
			switch jsonName {
			case "namespace", "componentNamespace", "projectName", "environmentName":
				fieldType = StringTypeWithLimit(dnsLabelStringLimit)
			default:
				fieldType = StringTypeWithLimit(defaultStringLimit)
			}
		} else if field.Type.Kind() == reflect.Slice {
			switch jsonName {
			case "envs":
				fieldType = apiservercel.NewListType(fieldType.ElemType, 1024)
			case "configEnvs", "secretEnvs":
				fieldType = apiservercel.NewListType(fieldType.ElemType, 1)
			case "configFileList", "secretFileList":
				fieldType = apiservercel.NewListType(fieldType.ElemType, 256)
			case "visibility":
				fieldType = apiservercel.NewListType(fieldType.ElemType, 4)
			}
		}

		required := !isOmitempty(field)

		fields[jsonName] = apiservercel.NewDeclField(jsonName, fieldType, required, nil, nil)
	}

	objType := apiservercel.NewObjectType(t.Name(), fields)

	// Update placeholder
	*placeholder = *objType
	return placeholder
}

func getJSONFieldName(field reflect.StructField) string {
	tag := field.Tag.Get("json")
	if tag == "" {
		return field.Name
	}
	name, _, _ := strings.Cut(tag, ",")
	if name == "" {
		return field.Name
	}
	return name
}

func isOmitempty(field reflect.StructField) bool {
	tag := field.Tag.Get("json")
	return strings.Contains(tag, "omitempty")
}
