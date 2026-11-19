// Copyright 2025 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package component

import (
	"fmt"
	"reflect"

	"github.com/google/cel-go/cel"
	apiextschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	apiservercel "k8s.io/apiserver/pkg/cel"

	"github.com/openchoreo/openchoreo/internal/pipeline/component/context"
	endpointpipeline "github.com/openchoreo/openchoreo/internal/pipeline/endpoint"
	"github.com/openchoreo/openchoreo/internal/template"
	"github.com/openchoreo/openchoreo/internal/validation/decltype"
	"github.com/openchoreo/openchoreo/internal/validation/schemautil"
)

// schemaBasedFields are populated from user-provided schemas, not reflection.
var schemaBasedFields = map[string]bool{
	"parameters":         true,
	"environmentConfigs": true,
}

// Cached field info derived from context types (excludes schema-based fields).
var (
	componentContextFields = decltype.ExtractFields(reflect.TypeFor[context.ComponentContext](), schemaBasedFields)
	traitContextFields     = decltype.ExtractFields(reflect.TypeFor[context.TraitContext](), schemaBasedFields)
	endpointContextFields  = decltype.ExtractFields(reflect.TypeFor[endpointpipeline.BaseContext](), schemaBasedFields)
)

// SchemaOptions provides schema configuration for CEL environment and validation.
// Used by both component and trait CEL environments.
type SchemaOptions struct {
	// ParametersSchema is the structural schema for parameters.
	// If nil, an empty object type will be used.
	ParametersSchema *apiextschema.Structural

	// EnvironmentConfigsSchema is the structural schema for environmentConfigs.
	// If nil, an empty object type will be used.
	EnvironmentConfigsSchema *apiextschema.Structural
}

// BuildComponentCELEnv creates a schema-aware CEL environment for component validation (exposed for tests).
func BuildComponentCELEnv(opts SchemaOptions) (*cel.Env, *apiservercel.DeclTypeProvider, error) {
	env, prov, _, err := BuildComponentCELEnvWithRoots(opts)
	return env, prov, err
}

// BuildComponentCELEnvWithRoots creates a schema-aware CEL environment and returns the roots map.
func BuildComponentCELEnvWithRoots(opts SchemaOptions) (*cel.Env, *apiservercel.DeclTypeProvider, map[string]*apiservercel.DeclType, error) {
	return buildCELEnv(componentContextFields, opts)
}

// BuildTraitCELEnv creates a schema-aware CEL environment for trait validation (exposed for tests).
func BuildTraitCELEnv(opts SchemaOptions) (*cel.Env, *apiservercel.DeclTypeProvider, error) {
	env, prov, _, err := BuildTraitCELEnvWithRoots(opts)
	return env, prov, err
}

// BuildTraitCELEnvWithRoots creates a schema-aware CEL environment and returns the roots map.
func BuildTraitCELEnvWithRoots(opts SchemaOptions) (*cel.Env, *apiservercel.DeclTypeProvider, map[string]*apiservercel.DeclType, error) {
	return buildCELEnv(traitContextFields, opts)
}

// BuildEndpointCELEnv creates a schema-aware CEL environment for endpoint validation.
//
// Deprecated: Use endpoint.BuildEndpointCELEnv directly. Retained for backwards compatibility.
func BuildEndpointCELEnv(opts SchemaOptions) (*cel.Env, *apiservercel.DeclTypeProvider, map[string]*apiservercel.DeclType, error) {
	baseEnv, err := cel.NewEnv(template.BaseCELExtensions()...)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to create base CEL environment: %w", err)
	}
	return buildCELEnvOn(baseEnv, endpointContextFields, opts)
}

// buildComponentValidator creates a schema-aware CEL validator for component validation.
func buildComponentValidator(opts SchemaOptions) (*CELValidator, error) {
	return buildValidatorFromFields(componentContextFields, opts)
}

// buildTraitValidator creates a schema-aware CEL validator for trait validation.
func buildTraitValidator(opts SchemaOptions) (*CELValidator, error) {
	return buildValidatorFromFields(traitContextFields, opts)
}

// buildEndpointValidator creates a schema-aware CEL validator for EndpointType validation.
func buildEndpointValidator(opts SchemaOptions) (*CELValidator, error) {
	env, provider, roots, err := BuildEndpointCELEnv(opts)
	if err != nil {
		return nil, err
	}
	return &CELValidator{
		baseEnv:      env,
		typeProvider: provider,
		rootTypes:    roots,
	}, nil
}

func buildValidatorFromFields(contextFields []decltype.FieldInfo, opts SchemaOptions) (*CELValidator, error) {
	env, provider, roots, err := buildCELEnv(contextFields, opts)
	if err != nil {
		return nil, fmt.Errorf("failed to create CEL environment: %w", err)
	}
	return &CELValidator{
		baseEnv:      env,
		typeProvider: provider,
		rootTypes:    roots,
	}, nil
}

// buildCELEnv creates a schema-aware CEL environment with the given context fields and schema options.
// Schema-based fields (parameters, environmentConfigs) are derived from the provided schemas.
// Reflection-based fields (metadata, workload, etc.) come from the contextFields slice.
func buildCELEnv(contextFields []decltype.FieldInfo, opts SchemaOptions) (*cel.Env, *apiservercel.DeclTypeProvider, map[string]*apiservercel.DeclType, error) {
	baseEnv, err := createBaseEnv()
	if err != nil {
		return nil, nil, nil, err
	}
	return buildCELEnvOn(baseEnv, contextFields, opts)
}

// buildCELEnvOn extends baseEnv with the schema-based and reflection-based variables.
func buildCELEnvOn(
	baseEnv *cel.Env, contextFields []decltype.FieldInfo, opts SchemaOptions,
) (*cel.Env, *apiservercel.DeclTypeProvider, map[string]*apiservercel.DeclType, error) {
	return schemautil.BuildCELEnv(schemautil.EnvSpec{
		BaseEnv: baseEnv,
		Schemas: []schemautil.NamedSchema{
			{Name: "parameters", Schema: opts.ParametersSchema, TypeName: "Parameters"},
			{Name: "environmentConfigs", Schema: opts.EnvironmentConfigsSchema, TypeName: "EnvironmentConfigs"},
		},
		ContextFields: contextFields,
	})
}

// createBaseEnv creates the base CEL environment with standard extensions.
func createBaseEnv() (*cel.Env, error) {
	baseEnvOpts := template.BaseCELExtensions()
	baseEnvOpts = append(baseEnvOpts, context.CELExtensions()...)
	return cel.NewEnv(baseEnvOpts...)
}
