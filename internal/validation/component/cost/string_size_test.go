// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package cost

import (
	"reflect"
	"testing"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/checker"
	apiservercel "k8s.io/apiserver/pkg/cel"

	"github.com/openchoreo/openchoreo/internal/pipeline/component/context"
	"github.com/openchoreo/openchoreo/internal/validation/component/decltype"
)

func TestPlatformStringTypeMaxElements(t *testing.T) {
	// Inspect apiservercel.StringType directly
	t.Logf("apiservercel.StringType: MaxElements=%d, MinSerializedSize=%d", 
		apiservercel.StringType.MaxElements, 
		apiservercel.StringType.MinSerializedSize)

	// Inspect reflected MetadataContext
	metaDecl := decltype.FromGoType(reflect.TypeFor[context.MetadataContext]())
	if metaDecl == nil {
		t.Fatalf("failed to reflect MetadataContext")
	}

	nameField, ok := metaDecl.Fields["name"]
	if !ok {
		t.Fatalf("name field not found on MetadataContext DeclType")
	}

	t.Logf("MetadataContext.name: DeclType=%v, MaxElements=%d, MinSerializedSize=%d", 
		nameField.Type, 
		nameField.Type.MaxElements, 
		nameField.Type.MinSerializedSize)

	if nameField.Type.MaxElements == 0 {
		t.Logf("CONFIRMED VULNERABILITY: Platform strings currently have MaxElements=0 (undercount risk)")
	}

	// Verify env integration with DeclTypeProvider
	provider := apiservercel.NewDeclTypeProvider(metaDecl)
	baseEnv, err := cel.NewEnv()
	if err != nil {
		t.Fatalf("failed to create base env: %v", err)
	}

	provOpts, err := provider.EnvOptions(baseEnv.CELTypeProvider())
	if err != nil {
		t.Fatalf("provider.EnvOptions failed: %v", err)
	}

	env, err := baseEnv.Extend(
		append(provOpts, cel.Variable("metadata", metaDecl.CelType()))...,
	)
	if err != nil {
		t.Fatalf("failed to extend env: %v", err)
	}

	ast, issues := env.Compile("metadata.name")
	if issues != nil && issues.Err() != nil {
		t.Fatalf("failed to compile: %v", issues.Err())
	}

	coster := NewStructuralCostEstimator(nil, nil)
	costEst, err := checker.Cost(ast.NativeRep(), coster)
	if err != nil {
		t.Fatalf("checker.Cost failed: %v", err)
	}

	t.Logf("metadata.name estimated cost: Min=%d, Max=%d", costEst.Min, costEst.Max)
}
