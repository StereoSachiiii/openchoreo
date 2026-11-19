// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package resource

import (
	"fmt"
	"reflect"

	"github.com/google/cel-go/cel"
	apiextschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	apiservercel "k8s.io/apiserver/pkg/cel"

	"github.com/openchoreo/openchoreo/internal/limits"
	resourcepipeline "github.com/openchoreo/openchoreo/internal/pipeline/resource"
	"github.com/openchoreo/openchoreo/internal/template"
	"github.com/openchoreo/openchoreo/internal/validation/decltype"
	"github.com/openchoreo/openchoreo/internal/validation/schemautil"
)

// schemaBasedFields are populated from user-provided schemas, not reflection.
// They are skipped during ExtractFields and declared separately based on the
// parameters / environmentConfigs schemas on the ResourceType being validated.
var schemaBasedFields = map[string]bool{
	"parameters":         true,
	"environmentConfigs": true,
}

// resourceContextFields are the CEL variables declared from the pipeline's
// BaseContext via reflection. Mirrors internal/validation/component.cel_env's
// componentContextFields: the single source of truth for the CEL surface lives
// on the pipeline's BaseContext type.
var resourceContextFields = decltype.ExtractFields(
	reflect.TypeFor[resourcepipeline.BaseContext](),
	schemaBasedFields,
)

// SchemaOptions provides schema configuration for resource CEL environment building.
// Mirrors internal/validation/component.SchemaOptions; defined here to keep the
// resource validation package self-contained.
type SchemaOptions struct {
	ParametersSchema         *apiextschema.Structural
	EnvironmentConfigsSchema *apiextschema.Structural
}

// BuildResourceCELEnv creates a schema-aware CEL environment for ResourceType
// and ClusterResourceType validation. The base env exposes metadata,
// parameters, environmentConfigs, and dataplane as top-level variables.
// applied.<id> is intentionally not in scope; ExtendEnvWithApplied layers it
// on for outputs and readyWhen validation.
func BuildResourceCELEnv(opts SchemaOptions) (*cel.Env, error) {
	env, _, _, err := BuildResourceCELEnvWithRoots(opts)
	return env, err
}

// BuildResourceCELEnvWithRoots creates a schema-aware CEL environment and returns the roots map.
func BuildResourceCELEnvWithRoots(opts SchemaOptions) (*cel.Env, *apiservercel.DeclTypeProvider, map[string]*apiservercel.DeclType, error) {
	baseEnv, err := createBaseEnv()
	if err != nil {
		return nil, nil, nil, err
	}

	appliedEntryType := apiservercel.NewObjectType("AppliedEntry", map[string]*apiservercel.DeclField{
		"status": apiservercel.NewDeclField("status", apiservercel.DynType, true, nil, nil),
	})
	appliedType := apiservercel.NewMapType(
		decltype.StringTypeWithLimit(limits.MaxKubernetesNameLength),
		appliedEntryType,
		limits.MaxAppliedResources,
	)

	return schemautil.BuildCELEnv(schemautil.EnvSpec{
		BaseEnv: baseEnv,
		Schemas: []schemautil.NamedSchema{
			{Name: "parameters", Schema: opts.ParametersSchema, TypeName: "Parameters"},
			{Name: "environmentConfigs", Schema: opts.EnvironmentConfigsSchema, TypeName: "EnvironmentConfigs"},
		},
		ContextFields: resourceContextFields,
		ExtraRoots: map[string]*apiservercel.DeclType{
			"applied": appliedType,
		},
	})
}

func buildResourceCELEnv(opts SchemaOptions) (*cel.Env, error) {
	return BuildResourceCELEnv(opts)
}

// ExtendEnvWithApplied returns a new env with applied in scope as
// map<string, AppliedEntry>.
func ExtendEnvWithApplied(env *cel.Env) (*cel.Env, error) {
	return extendEnvWithApplied(env)
}

// extendEnvWithApplied returns a new env with applied in scope as
// map<string, AppliedEntry>. AppliedEntry has a single Dyn-typed status field;
// the schema beneath status is not type-checked because operators populate it
// freely. Verifying that an applied.<id> reference matches a declared
// resources[].id is a separate AST-walk concern handled by the caller.
func extendEnvWithApplied(env *cel.Env) (*cel.Env, error) {
	appliedEntryType := apiservercel.NewObjectType("AppliedEntry", map[string]*apiservercel.DeclField{
		"status": apiservercel.NewDeclField("status", apiservercel.DynType, true, nil, nil),
	})

	// Only the named Object value type goes through the DeclTypeProvider.
	// The map wrapper is constructed inline at the cel.Type level: registering
	// a fresh apiservercel.MapType against an env that already carries
	// map<string, string> (from metadata.labels / metadata.annotations) would
	// trigger "type map definition differs between CEL environment and type
	// provider" because the provider tries to redefine the unnamed map type.
	provider := apiservercel.NewDeclTypeProvider(appliedEntryType)
	providerOpts, err := provider.EnvOptions(env.CELTypeProvider())
	if err != nil {
		return nil, fmt.Errorf("create applied type provider: %w", err)
	}

	opts := make([]cel.EnvOption, 0, 1+len(providerOpts))
	opts = append(opts, cel.Variable("applied", cel.MapType(cel.StringType, appliedEntryType.CelType())))
	opts = append(opts, providerOpts...)

	return env.Extend(opts...)
}

// createBaseEnv creates the base CEL environment with the standard
// template extensions (oc_omit and friends). Component-specific helpers
// (configurations.*, dependencies.*, connections.*) are deliberately absent because
// those are component-render-time concerns and have no meaning for resource
// templates.
func createBaseEnv() (*cel.Env, error) {
	return cel.NewEnv(template.BaseCELExtensions()...)
}
