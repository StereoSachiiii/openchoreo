// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package cost

import (
	"reflect"
	"testing"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/checker"
	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/ext"
	apiservercel "k8s.io/apiserver/pkg/cel"

	"github.com/openchoreo/openchoreo/internal/limits"
	"github.com/openchoreo/openchoreo/internal/pipeline/component/context"
	"github.com/openchoreo/openchoreo/internal/validation/decltype"
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

	coster := NewDeclTypeCostEstimator(map[string]*apiservercel.DeclType{"metadata": metaDecl})
	costEst, err := checker.Cost(ast.NativeRep(), coster)
	if err != nil {
		t.Fatalf("checker.Cost failed: %v", err)
	}

	t.Logf("metadata.name estimated cost: Min=%d, Max=%d", costEst.Min, costEst.Max)
}

func TestUnpathedCollectionSizeEstimation(t *testing.T) {
	coster := NewDeclTypeCostEstimator(nil)

	// 1. List of strings without path (e.g. from split or comprehension)
	listNode := NewTypedAstNode(types.NewListType(types.StringType))
	sizeEst := coster.EstimateSize(listNode)
	if sizeEst == nil || sizeEst.Max == 0 || sizeEst.Max > uint64(limits.DefaultSchemaMaxItems) {
		t.Fatalf("expected bounded size for unpathed list, got: %+v", sizeEst)
	}

	// 2. Map of string->string without path
	mapNode := NewTypedAstNode(types.NewMapType(types.StringType, types.StringType))
	mapEst := coster.EstimateSize(mapNode)
	if mapEst == nil || mapEst.Max == 0 || mapEst.Max > uint64(limits.DefaultSchemaMaxProperties) {
		t.Fatalf("expected bounded size for unpathed map, got: %+v", mapEst)
	}

	// 3. EndpointResource list bound check
	epResType := types.NewObjectType("schemaextract.EndpointResource")
	epListNode := NewTypedAstNode(types.NewListType(epResType))
	epEst := coster.EstimateSize(epListNode)
	if epEst == nil || epEst.Max != uint64(limits.MaxEndpointResourcesPerEp) {
		t.Fatalf("expected size %d for EndpointResource list, got: %+v", limits.MaxEndpointResourcesPerEp, epEst)
	}
}

func TestSplitCostEstimation(t *testing.T) {
	metaDecl := decltype.FromGoType(reflect.TypeFor[context.MetadataContext]())
	provider := apiservercel.NewDeclTypeProvider(metaDecl)
	baseEnv, err := cel.NewEnv(ext.Strings())
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

	ast, issues := env.Compile("metadata.name.split('/').map(s, s).size()")
	if issues != nil && issues.Err() != nil {
		t.Fatalf("failed to compile: %v", issues.Err())
	}

	coster := NewDeclTypeCostEstimator(map[string]*apiservercel.DeclType{"metadata": metaDecl})
	costEst, err := checker.Cost(ast.NativeRep(), coster)
	if err != nil {
		t.Fatalf("checker.Cost failed: %v", err)
	}

	t.Logf("split.map.size estimated cost: Min=%d, Max=%d", costEst.Min, costEst.Max)
}

func TestSchemaVsPlatformStringFallback(t *testing.T) {
	unboundedStrObj := apiservercel.NewObjectType("Root", map[string]*apiservercel.DeclField{
		"str": {Type: apiservercel.StringType},
	})
	coster := NewDeclTypeCostEstimator(map[string]*apiservercel.DeclType{
		"parameters":         unboundedStrObj,
		"environmentConfigs": unboundedStrObj,
		"customPlatformRoot": unboundedStrObj,
	})

	// Schema roots without MaxElements (when ActiveSchemaBounds is disabled) fall back to DefaultMaxLength (1.5 MiB - 2)
	paramEst := coster.EstimateSize(NewPathAstNode("parameters", "str"))
	if paramEst == nil || paramEst.Max != DefaultMaxLength {
		t.Fatalf("expected DefaultMaxLength (%d) for unbounded parameters string, got: %+v", DefaultMaxLength, paramEst)
	}

	envCfgEst := coster.EstimateSize(NewPathAstNode("environmentConfigs", "str"))
	if envCfgEst == nil || envCfgEst.Max != DefaultMaxLength {
		t.Fatalf("expected DefaultMaxLength (%d) for unbounded environmentConfigs string, got: %+v", DefaultMaxLength, envCfgEst)
	}

	// Non-schema platform roots with unannotated apiservercel.StringType fall back to assumedStringLength (253)
	platEst := coster.EstimateSize(NewPathAstNode("customPlatformRoot", "str"))
	if platEst == nil || platEst.Max != assumedStringLength {
		t.Fatalf("expected assumedStringLength (%d) for platform string, got: %+v", assumedStringLength, platEst)
	}
}
