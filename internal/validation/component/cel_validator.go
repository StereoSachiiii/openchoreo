// Copyright 2025 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package component

import (
	"fmt"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/types"
	apiservercel "k8s.io/apiserver/pkg/cel"

	"strings"
)

// ResourceType indicates which type of resource is being validated
type ResourceType int

const (
	// ComponentTypeResource indicates validation for ComponentType resources
	ComponentTypeResource ResourceType = iota
	// TraitResource indicates validation for Trait resources
	TraitResource
)

// CELValidator provides comprehensive CEL expression validation with AST analysis
type CELValidator struct {
	baseEnv      *cel.Env
	typeProvider *apiservercel.DeclTypeProvider
	rootTypes    map[string]*apiservercel.DeclType
}

// NewCELValidator creates a validator for the specified resource type.
// The validator uses different environments for ComponentType vs Trait resources
// to enforce proper variable access (e.g., traits have access to trait context variable).
// Provides schema-aware type checking when schemas are provided in opts.
func NewCELValidator(resourceType ResourceType, opts SchemaOptions) (*CELValidator, error) {
	var env *cel.Env
	var provider *apiservercel.DeclTypeProvider
	var rootTypes map[string]*apiservercel.DeclType
	var err error

	switch resourceType {
	case ComponentTypeResource:
		env, provider, rootTypes, err = buildComponentCELEnvWithRoots(opts)
	case TraitResource:
		env, provider, rootTypes, err = buildTraitCELEnvWithRoots(opts)
	default:
		return nil, fmt.Errorf("unknown resource type: %v", resourceType)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to create CEL environment: %w", err)
	}

	return &CELValidator{
		baseEnv:      env,
		typeProvider: provider,
		rootTypes:    rootTypes,
	}, nil
}

// GetRootTypes returns the root DeclTypes for cost estimation.
func (v *CELValidator) GetRootTypes() map[string]*apiservercel.DeclType {
	return v.rootTypes
}

// ValidateExpression validates a CEL expression with the base environment
func (v *CELValidator) ValidateExpression(expr string) error {
	return v.ValidateWithEnv(expr, v.baseEnv)
}

// ValidateWithEnv validates a CEL expression with a specific environment and estimates its cost.
// This is used when the environment has been extended with forEach variables.
func (v *CELValidator) ValidateWithEnv(expr string, env *cel.Env) error {
	parsed, issues := env.Parse(expr)
	if issues != nil && issues.Err() != nil {
		return fmt.Errorf("parse error: %w", issues.Err())
	}

	_, issues = env.Check(parsed)
	if issues != nil && issues.Err() != nil {
		return fmt.Errorf("type check error: %w", issues.Err())
	}

	// TODO: Admission cost validation will be re-enabled once calibrated against corpus and backed by DeclType estimator
	return nil
}

// ValidateBooleanExpression ensures the expression returns a boolean value.
// Used for includeWhen conditions and where filters.
func (v *CELValidator) ValidateBooleanExpression(expr string, env *cel.Env) error {
	parsed, issues := env.Parse(expr)
	if issues != nil && issues.Err() != nil {
		return fmt.Errorf("parse error: %w", issues.Err())
	}

	checked, issues := env.Check(parsed)
	if issues != nil && issues.Err() != nil {
		return fmt.Errorf("type check error: %w", issues.Err())
	}

	outputType := checked.OutputType()
	if !outputType.IsExactType(cel.BoolType) && outputType != cel.DynType {
		return fmt.Errorf("expression must return boolean, got %s", outputType)
	}

	return nil
}

// ValidateIterableExpression ensures the expression returns a list or map (for forEach).
func (v *CELValidator) ValidateIterableExpression(expr string, env *cel.Env) error {
	parsed, issues := env.Parse(expr)
	if issues != nil && issues.Err() != nil {
		return fmt.Errorf("parse error: %w", issues.Err())
	}

	checked, issues := env.Check(parsed)
	if issues != nil && issues.Err() != nil {
		return fmt.Errorf("type check error: %w", issues.Err())
	}

	outputType := checked.OutputType()
	kind := outputType.Kind()

	// Allow DynType since it could be iterable at runtime
	if kind != types.ListKind && kind != types.MapKind && outputType != cel.DynType {
		return fmt.Errorf("forEach expression must return list or map, got %s", outputType)
	}

	return nil
}

// GetBaseEnv returns the base CEL environment for this validator
func (v *CELValidator) GetBaseEnv() *cel.Env {
	return v.baseEnv
}

// GetTypeProvider returns the DeclTypeProvider for this validator.
// Used by forEach analysis to resolve map value types for field validation.
func (v *CELValidator) GetTypeProvider() *apiservercel.DeclTypeProvider {
	return v.typeProvider
}

// ResourceEnvironment returns the CEL environment for a specific resource,
// extending the base environment with loop variables when forEach is configured.
// This is the single environment factory specified in Project 701 §4.1.
func (v *CELValidator) ResourceEnvironment(forEachExpr string, varName string) (*cel.Env, error) {
	env, _, err := v.ResourceEnvironmentWithDeclType(forEachExpr, varName)
	return env, err
}

// ResourceEnvironmentWithDeclType returns the CEL environment and the loop variable's DeclType (if any)
// for a specific resource, extending the base environment with loop variables when forEach is configured.
func (v *CELValidator) ResourceEnvironmentWithDeclType(forEachExpr string, varName string) (*cel.Env, *apiservercel.DeclType, error) {
	if forEachExpr == "" {
		return v.baseEnv, nil, nil
	}

	// Unwrap template expression ${...} if wrapped
	expr := forEachExpr
	if trimmed, ok := strings.CutPrefix(strings.TrimSpace(expr), "${"); ok {
		if inner, ok := strings.CutSuffix(trimmed, "}"); ok {
			expr = strings.TrimSpace(inner)
		}
	}

	info, err := analyzeForEachExpression(expr, varName, v.baseEnv)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to analyze forEach expression: %w", err)
	}
	if info == nil {
		return v.baseEnv, nil, nil
	}

	extEnv, err := extendEnvWithForEach(v.baseEnv, info, v.typeProvider)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to extend CEL environment with forEach: %w", err)
	}
	return extEnv, info.VarDeclType, nil
}

