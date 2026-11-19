// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package endpoint

import (
	"fmt"
	"reflect"

	"github.com/google/cel-go/cel"
	apiextschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	apiservercel "k8s.io/apiserver/pkg/cel"

	endpointpipeline "github.com/openchoreo/openchoreo/internal/pipeline/endpoint"
	"github.com/openchoreo/openchoreo/internal/template"
	"github.com/openchoreo/openchoreo/internal/validation/decltype"
	"github.com/openchoreo/openchoreo/internal/validation/schemautil"
)

var schemaBasedFields = map[string]bool{
	"parameters":         true,
	"environmentConfigs": true,
}

var endpointContextFields = decltype.ExtractFields(
	reflect.TypeFor[endpointpipeline.BaseContext](),
	schemaBasedFields,
)

// SchemaOptions provides schema configuration for endpoint CEL environment building.
type SchemaOptions struct {
	ParametersSchema         *apiextschema.Structural
	EnvironmentConfigsSchema *apiextschema.Structural
}

// BuildEndpointCELEnv creates a schema-aware CEL environment for endpoint validation.
func BuildEndpointCELEnv(opts SchemaOptions) (*cel.Env, *apiservercel.DeclTypeProvider, map[string]*apiservercel.DeclType, error) {
	baseEnv, err := cel.NewEnv(template.BaseCELExtensions()...)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to create base CEL environment: %w", err)
	}

	return schemautil.BuildCELEnv(schemautil.EnvSpec{
		BaseEnv: baseEnv,
		Schemas: []schemautil.NamedSchema{
			{Name: "parameters", Schema: opts.ParametersSchema, TypeName: "Parameters"},
			{Name: "environmentConfigs", Schema: opts.EnvironmentConfigsSchema, TypeName: "EnvironmentConfigs"},
		},
		ContextFields: endpointContextFields,
	})
}
