// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package cost_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/cel-go/cel"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
	"k8s.io/apimachinery/pkg/util/validation/field"
	apiservercel "k8s.io/apiserver/pkg/cel"
	k8syaml "sigs.k8s.io/yaml"

	"github.com/openchoreo/openchoreo/api/v1alpha1"
	"github.com/openchoreo/openchoreo/internal/limits"
	"github.com/openchoreo/openchoreo/internal/template"
	"github.com/openchoreo/openchoreo/internal/validation/component"
	"github.com/openchoreo/openchoreo/internal/validation/cost"
	"github.com/openchoreo/openchoreo/internal/validation/schemautil"
)

// TestCapsParity verifies that static and runtime paths share identical limits.
func TestCapsParity(t *testing.T) {
	// 1. Lists.range cap parity
	assert.Equal(t, int64(10_000), limits.MaxListsRangeSize)

	// 2. ForEach caps parity (MaxForEachItemsPerLoop = 250, MaxForEachItemsPerRender = 2000)
	assert.Equal(t, uint64(250), limits.MaxForEachItemsPerLoop)
	assert.Equal(t, uint64(2_000), limits.MaxForEachItemsPerRender)

	// 3. Render budget factor & ReconcileBudget derivation
	assert.Equal(t, uint64(5), limits.RenderBudgetFactor)
	assert.Equal(t, 4_000_000*limits.RenderBudgetFactor, limits.ReconcileBudget(4_000_000))
	assert.Equal(t, limits.DefaultCELCostLimit*limits.RenderBudgetFactor, limits.ReconcileBudget(0))
}

// TestSoundnessAgainstDeclaredBounds verifies that declared bounds on collections
// are honored by DeclTypeCostEstimator without any clamping.
func TestSoundnessAgainstDeclaredBounds(t *testing.T) {
	fiveThousand := int64(5000)
	itemType := apiservercel.NewObjectType("Item", map[string]*apiservercel.DeclField{
		"port": {Type: apiservercel.IntType},
	})
	listType := apiservercel.NewListType(itemType, fiveThousand)
	rootType := apiservercel.NewObjectType("Params", map[string]*apiservercel.DeclField{
		"rules": {Type: listType},
	})

	estimator := cost.NewDeclTypeCostEstimator(map[string]*apiservercel.DeclType{
		"parameters": rootType,
	})

	size := estimator.EstimateSize(cost.NewPathAstNode("parameters", "rules"))
	require.NotNil(t, size)
	assert.Equal(t, uint64(5000), size.Max, "declared 5000 on complex list must be honored without clamp")

	// Counterfactual: if a clamp lowered 5000, size.Max would be <= 250
	assert.False(t, size.Max <= 250, "estimator must not clamp declared bounds")
}

// TestCollectionSizeIsCappedByObjectSize verifies that a list with no usable declared bound is
// capped by how many of its smallest element fit in cost.MaxObjectSizeBytes. The expectations
// are derived from the element type, not hard-coded, so they follow the ceiling if it changes.
func TestCollectionSizeIsCappedByObjectSize(t *testing.T) {
	estimate := func(list *apiservercel.DeclType) uint64 {
		root := apiservercel.NewObjectType("Params", map[string]*apiservercel.DeclField{
			"items": {Type: list},
		})
		estimator := cost.NewDeclTypeCostEstimator(map[string]*apiservercel.DeclType{"parameters": root})
		size := estimator.EstimateSize(cost.NewPathAstNode("parameters", "items"))
		require.NotNil(t, size)
		return size.Max
	}
	// fits reports whether n comma-separated elements of elemSize bytes fit in one object.
	fits := func(n uint64, elemSize uint64) bool {
		return n*(elemSize+1)+2 <= uint64(cost.MaxObjectSizeBytes)
	}

	elems := []struct {
		name    string
		elem    *apiservercel.DeclType
		minSize uint64 // smallest JSON encoding of one element
	}{
		{"int", apiservercel.IntType, 1},
		{"string", apiservercel.StringType, 2},
		{"bool", apiservercel.BoolType, 4},
	}

	counts := make(map[string]uint64, len(elems))
	for _, e := range elems {
		unbounded := estimate(apiservercel.NewListType(e.elem, 0))
		counts[e.name] = unbounded
		assert.True(t, fits(unbounded, e.minSize), "%s list: %d items must fit in the object", e.name, unbounded)
		assert.False(t, fits(unbounded+1, e.minSize), "%s list: %d items is not the largest that fits", e.name, unbounded)

		// A declared bound too large to physically fit is capped, like an unbounded one.
		assert.Equal(t, unbounded, estimate(apiservercel.NewListType(e.elem, cost.MaxObjectSizeBytes)),
			"%s list: oversized declared bound must be capped", e.name)
		// A declared bound tighter than the ceiling is kept as declared.
		assert.Equal(t, uint64(7), estimate(apiservercel.NewListType(e.elem, 7)),
			"%s list: tighter declared bound must be honored", e.name)
	}

	// Smaller elements mean more of them fit.
	assert.Greater(t, counts["int"], counts["string"])
	assert.Greater(t, counts["string"], counts["bool"])
}

const kindComponentType = "ComponentType"

// TestEnvParity compiles every shipped sample's CEL expressions through both:
// 1) The static validator's BaseEnv (GetBaseEnv())
// 2) The runtime engine's BaseCELExtensions()
func TestEnvParity(t *testing.T) {
	samplesDir := "../../../samples"
	absPath, err := filepath.Abs(samplesDir)
	require.NoError(t, err)

	var yamlFiles []string
	err = filepath.Walk(absPath, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if strings.HasSuffix(path, ".yaml") || strings.HasSuffix(path, ".yml") {
			yamlFiles = append(yamlFiles, path)
		}
		return nil
	})
	require.NoError(t, err)

	engineBaseEnv, err := cel.NewEnv(template.BaseCELExtensions()...)
	require.NoError(t, err, "failed to build engine BaseCELExtensions env")

	testedCount := 0

	for _, file := range yamlFiles {
		content, err := os.ReadFile(file)
		if err != nil {
			continue
		}

		docs := strings.Split(string(content), "\n---")
		for _, doc := range docs {
			if strings.TrimSpace(doc) == "" {
				continue
			}

			var header struct {
				Kind string `yaml:"kind"`
			}
			if yaml.Unmarshal([]byte(doc), &header) != nil {
				continue
			}

			if header.Kind == kindComponentType || header.Kind == "ClusterComponentType" {
				var ctSpec v1alpha1.ComponentTypeSpec
				if header.Kind == kindComponentType {
					var ct v1alpha1.ComponentType
					jsonData, err := k8syaml.YAMLToJSON([]byte(doc))
					if err != nil || json.Unmarshal(jsonData, &ct) != nil {
						continue
					}
					ctSpec = ct.Spec
				} else {
					var cct v1alpha1.ClusterComponentType
					jsonData, err := k8syaml.YAMLToJSON([]byte(doc))
					if err != nil || json.Unmarshal(jsonData, &cct) != nil {
						continue
					}
					ctSpec = cct.Spec.ToComponentTypeSpec()
				}

				paramSchema, envConfigSchema, _ := schemautil.ExtractAndValidateSchemas(
					ctSpec.Parameters, ctSpec.EnvironmentConfigs, field.NewPath("spec"),
				)

				val, err := component.NewCELValidator(component.ComponentTypeResource, component.SchemaOptions{
					ParametersSchema:         paramSchema,
					EnvironmentConfigsSchema: envConfigSchema,
				})
				if err != nil {
					continue
				}

				staticBaseEnv := val.GetBaseEnv()

				// Extract expressions from validations
				for _, r := range ctSpec.Validations {
					if r.Rule != "" {
						expr := strings.TrimSuffix(strings.TrimPrefix(r.Rule, "${"), "}")
						checkBothEnvs(t, expr, staticBaseEnv, engineBaseEnv)
						testedCount++
					}
				}
				for _, r := range ctSpec.PreRenderValidations {
					if r.Rule != "" {
						expr := strings.TrimSuffix(strings.TrimPrefix(r.Rule, "${"), "}")
						checkBothEnvs(t, expr, staticBaseEnv, engineBaseEnv)
						testedCount++
					}
				}
			}
		}
	}

	t.Logf("Tested env parity over %d expressions", testedCount)
	assert.Greater(t, testedCount, 0)
}

func checkBothEnvs(t *testing.T, expr string, staticEnv, engineEnv *cel.Env) {
	t.Helper()
	astStatic, issuesStatic := staticEnv.Parse(expr)
	astEngine, issuesEngine := engineEnv.Parse(expr)

	if issuesStatic != nil && issuesStatic.Err() != nil {
		assert.Error(t, issuesEngine.Err(), "expr %q parse failed in static but succeeded in engine", expr)
		return
	}
	require.NoError(t, issuesEngine.Err(), "expr %q parsed in static but failed in engine", expr)
	assert.NotNil(t, astStatic)
	assert.NotNil(t, astEngine)
}
