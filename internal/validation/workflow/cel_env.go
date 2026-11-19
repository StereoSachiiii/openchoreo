// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package workflow

import (
	"fmt"
	"reflect"

	"github.com/google/cel-go/cel"
	apiextschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	apiservercel "k8s.io/apiserver/pkg/cel"

	workflowpipeline "github.com/openchoreo/openchoreo/internal/pipeline/workflow"
	"github.com/openchoreo/openchoreo/internal/template"
	"github.com/openchoreo/openchoreo/internal/validation/decltype"
	"github.com/openchoreo/openchoreo/internal/validation/schemautil"
)

var workflowSchemaBasedFields = map[string]bool{
	"parameters": true,
}

var workflowContextFields = decltype.ExtractFields(
	reflect.TypeFor[workflowpipeline.WorkflowCELContext](),
	workflowSchemaBasedFields,
)

// SchemaOptions provides schema configuration for workflow CEL environment building.
type SchemaOptions struct {
	ParametersSchema *apiextschema.Structural
}

// BuildWorkflowCELEnv creates a schema-aware CEL environment for Workflow and ClusterWorkflow validation.
func BuildWorkflowCELEnv(opts SchemaOptions) (*cel.Env, *apiservercel.DeclTypeProvider, map[string]*apiservercel.DeclType, error) {
	baseEnv, err := cel.NewEnv(template.BaseCELExtensions()...)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("create base workflow CEL env: %w", err)
	}

	return schemautil.BuildCELEnv(schemautil.EnvSpec{
		BaseEnv: baseEnv,
		Schemas: []schemautil.NamedSchema{
			{Name: "parameters", Schema: opts.ParametersSchema, TypeName: "Parameters"},
		},
		ContextFields: workflowContextFields,
	})
}
