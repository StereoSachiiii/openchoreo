// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package schemautil

import (
	"fmt"

	"github.com/google/cel-go/cel"
	apiextschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	apiservercel "k8s.io/apiserver/pkg/cel"

	"github.com/openchoreo/openchoreo/internal/validation/decltype"
)

// NamedSchema pairs a variable name with its structural schema and the DeclType type name.
type NamedSchema struct {
	Name     string
	Schema   *apiextschema.Structural
	TypeName string
}

// EnvSpec defines the specification for building a schema-aware CEL environment.
type EnvSpec struct {
	// BaseEnv is the underlying CEL environment with base functions/extensions.
	// If nil, callers must provide one.
	BaseEnv *cel.Env

	// Schemas contains schema-based variables (e.g. parameters, environmentConfigs).
	Schemas []NamedSchema

	// ContextFields contains reflection-based variables (e.g. metadata, workload).
	ContextFields []decltype.FieldInfo

	// ExtraRoots contains any additional root decltypes (e.g. applied).
	ExtraRoots map[string]*apiservercel.DeclType
}

// BuildCELEnv creates a schema-aware CEL environment from an EnvSpec.
// It registers both schema-derived and reflection-derived variables onto BaseEnv
// and configures the DeclTypeProvider.
func BuildCELEnv(spec EnvSpec) (*cel.Env, *apiservercel.DeclTypeProvider, map[string]*apiservercel.DeclType, error) {
	if spec.BaseEnv == nil {
		return nil, nil, nil, fmt.Errorf("BaseEnv is required in EnvSpec")
	}

	numFields := len(spec.Schemas) + len(spec.ContextFields) + len(spec.ExtraRoots)
	declTypes := make([]*apiservercel.DeclType, 0, numFields)
	varOpts := make([]cel.EnvOption, 0, numFields)
	roots := make(map[string]*apiservercel.DeclType, numFields)

	for _, ns := range spec.Schemas {
		dt := SchemaToDeclType(ns.Schema, ns.TypeName)
		declTypes = append(declTypes, dt)
		roots[ns.Name] = dt
		varOpts = append(varOpts, cel.Variable(ns.Name, dt.CelType()))
	}

	for _, f := range spec.ContextFields {
		declTypes = append(declTypes, f.DeclType)
		roots[f.Name] = f.DeclType
		varOpts = append(varOpts, cel.Variable(f.Name, f.DeclType.CelType()))
	}

	for name, dt := range spec.ExtraRoots {
		roots[name] = dt
	}

	provider := apiservercel.NewDeclTypeProvider(declTypes...)
	providerOpts, err := provider.EnvOptions(spec.BaseEnv.CELTypeProvider())
	if err != nil {
		return nil, nil, nil, fmt.Errorf("provider EnvOptions: %w", err)
	}
	varOpts = append(varOpts, providerOpts...)

	env, err := spec.BaseEnv.Extend(varOpts...)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("extend CEL env: %w", err)
	}

	return env, provider, roots, nil
}
