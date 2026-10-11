// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package cost_test

import (
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/google/cel-go/checker"
	"gopkg.in/yaml.v3"
	apiextschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	"k8s.io/apiextensions-apiserver/pkg/apiserver/schema/cel/model"
	apiservercel "k8s.io/apiserver/pkg/cel"

	"github.com/openchoreo/openchoreo/internal/pipeline/component/context"
	"github.com/openchoreo/openchoreo/internal/schema"
	"github.com/openchoreo/openchoreo/internal/template"
	"github.com/openchoreo/openchoreo/internal/validation/component"
	"github.com/openchoreo/openchoreo/internal/validation/component/cost"
	"github.com/openchoreo/openchoreo/internal/validation/component/decltype"
)

type rawResourceTemplate struct {
	ID          string `yaml:"id"`
	IncludeWhen string `yaml:"includeWhen"`
	ForEach     string `yaml:"forEach"`
	Var         string `yaml:"var"`
	Template    any    `yaml:"template"`
}

type rawValidationRule struct {
	Rule    string `yaml:"rule"`
	Message string `yaml:"message"`
}

type rawComponentTypeDoc struct {
	APIVersion string `yaml:"apiVersion"`
	Kind       string `yaml:"kind"`
	Metadata   struct {
		Name string `yaml:"name"`
	} `yaml:"metadata"`
	Spec struct {
		Parameters struct {
			OpenAPIV3Schema map[string]any `yaml:"openAPIV3Schema"`
		} `yaml:"parameters"`
		EnvironmentConfigs struct {
			OpenAPIV3Schema map[string]any `yaml:"openAPIV3Schema"`
		} `yaml:"environmentConfigs"`
		Validations          []rawValidationRule   `yaml:"validations"`
		PreRenderValidations []rawValidationRule   `yaml:"preRenderValidations"`
		Resources            []rawResourceTemplate `yaml:"resources"`
	} `yaml:"spec"`
}

type expressionCostRecord struct {
	Component     string
	ResourceID    string
	Expression    string
	MinCost       uint64
	MaxCost       uint64
	IsMaxUint64   bool
	UnknownSource string
}

type skippedExpressionRecord struct {
	Component  string
	ResourceID string
	Expression string
	Error      error
}

func TestCorpusStaticCostEvaluation(t *testing.T) {
	samplesDir := "../../../../samples"
	absPath, err := filepath.Abs(samplesDir)
	if err != nil {
		t.Fatalf("failed to resolve samples dir: %v", err)
	}

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
	if err != nil {
		t.Fatalf("failed to walk samples: %v", err)
	}

	var records []expressionCostRecord
	var skipped []skippedExpressionRecord
	totalExtracted := 0

	for _, file := range yamlFiles {
		content, err := os.ReadFile(file)
		if err != nil {
			continue
		}

		docs := strings.Split(string(content), "\n---")
		for _, doc := range docs {
			var ct rawComponentTypeDoc
			if err := yaml.Unmarshal([]byte(doc), &ct); err != nil {
				continue
			}

			if ct.Kind != "ComponentType" && ct.Kind != "ClusterComponentType" {
				continue
			}

			var paramStruct *apiextschema.Structural
			if len(ct.Spec.Parameters.OpenAPIV3Schema) > 0 {
				paramStruct, _ = schema.OpenAPIV3ToStructural(ct.Spec.Parameters.OpenAPIV3Schema)
			}

			var envConfigStruct *apiextschema.Structural
			if len(ct.Spec.EnvironmentConfigs.OpenAPIV3Schema) > 0 {
				envConfigStruct, _ = schema.OpenAPIV3ToStructural(ct.Spec.EnvironmentConfigs.OpenAPIV3Schema)
			}

			// Use the production validator constructor
			validator, err := component.NewCELValidator(component.ComponentTypeResource, component.SchemaOptions{
				ParametersSchema:         paramStruct,
				EnvironmentConfigsSchema: envConfigStruct,
			})
			if err != nil {
				t.Fatalf("failed to create production CELValidator for %s: %v", ct.Metadata.Name, err)
			}

			baseEnv := validator.GetBaseEnv()

			// Build root types for DeclTypeCostEstimator
			roots := make(map[string]*apiservercel.DeclType)
			if paramStruct != nil {
				roots["parameters"] = model.SchemaDeclType(paramStruct, false)
			} else {
				roots["parameters"] = apiservercel.NewObjectType("Parameters", map[string]*apiservercel.DeclField{})
			}
			if envConfigStruct != nil {
				roots["environmentConfigs"] = model.SchemaDeclType(envConfigStruct, false)
			} else {
				roots["environmentConfigs"] = apiservercel.NewObjectType("EnvironmentConfigs", map[string]*apiservercel.DeclField{})
			}
			schemaBasedFields := map[string]bool{"parameters": true, "environmentConfigs": true}
			reflectedFields := decltype.ExtractFields(reflect.TypeFor[context.ComponentContext](), schemaBasedFields)
			for _, rf := range reflectedFields {
				roots[rf.Name] = rf.DeclType
			}

			coster := cost.NewDeclTypeCostEstimator(roots)

			// 1. Validations & preRenderValidations
			allRules := append(ct.Spec.Validations, ct.Spec.PreRenderValidations...)
			for _, r := range allRules {
				exprs, err := template.FindCELExpressions(r.Rule)
				if err != nil {
					t.Fatalf("failed to parse expressions in rule %q: %v", r.Rule, err)
				}
				for _, expr := range exprs {
					totalExtracted++
					ast, issues := baseEnv.Compile(expr.InnerExpr)
					if issues != nil && issues.Err() != nil {
						skipped = append(skipped, skippedExpressionRecord{
							Component:  ct.Metadata.Name,
							ResourceID: "validation",
							Expression: expr.InnerExpr,
							Error:      issues.Err(),
						})
						continue
					}

					costEst, err := checker.Cost(ast.NativeRep(), coster)
					if err != nil {
						skipped = append(skipped, skippedExpressionRecord{
							Component:  ct.Metadata.Name,
							ResourceID: "validation",
							Expression: expr.InnerExpr,
							Error:      err,
						})
						continue
					}

					rec := expressionCostRecord{
						Component:   ct.Metadata.Name,
						ResourceID:  "validation",
						Expression:  expr.InnerExpr,
						MinCost:     costEst.Min,
						MaxCost:     costEst.Max,
						IsMaxUint64: costEst.Max == math.MaxUint64,
					}
					records = append(records, rec)
				}
			}

			// 2. Resources
			for _, res := range ct.Spec.Resources {
				resEnv := baseEnv

				// includeWhen is evaluated in baseEnv
				if res.IncludeWhen != "" {
					ex, err := template.FindCELExpressions(res.IncludeWhen)
					if err != nil {
						t.Fatalf("failed to parse includeWhen %q: %v", res.IncludeWhen, err)
					}
					for _, e := range ex {
						totalExtracted++
						ast, issues := baseEnv.Compile(e.InnerExpr)
						if issues != nil && issues.Err() != nil {
							skipped = append(skipped, skippedExpressionRecord{
								Component:  ct.Metadata.Name,
								ResourceID: res.ID + "/includeWhen",
								Expression: e.InnerExpr,
								Error:      issues.Err(),
							})
							continue
						}
						costEst, err := checker.Cost(ast.NativeRep(), coster)
						if err != nil {
							skipped = append(skipped, skippedExpressionRecord{
								Component:  ct.Metadata.Name,
								ResourceID: res.ID + "/includeWhen",
								Expression: e.InnerExpr,
								Error:      err,
							})
							continue
						}
						records = append(records, expressionCostRecord{
							Component:   ct.Metadata.Name,
							ResourceID:  res.ID + "/includeWhen",
							Expression:  e.InnerExpr,
							MinCost:     costEst.Min,
							MaxCost:     costEst.Max,
							IsMaxUint64: costEst.Max == math.MaxUint64,
						})
					}
				}

				// forEach is evaluated in baseEnv
				if res.ForEach != "" {
					ex, err := template.FindCELExpressions(res.ForEach)
					if err != nil {
						t.Fatalf("failed to parse forEach %q: %v", res.ForEach, err)
					}
					for _, e := range ex {
						totalExtracted++
						ast, issues := baseEnv.Compile(e.InnerExpr)
						if issues != nil && issues.Err() != nil {
							skipped = append(skipped, skippedExpressionRecord{
								Component:  ct.Metadata.Name,
								ResourceID: res.ID + "/forEach",
								Expression: e.InnerExpr,
								Error:      issues.Err(),
							})
							continue
						}
						costEst, err := checker.Cost(ast.NativeRep(), coster)
						if err != nil {
							skipped = append(skipped, skippedExpressionRecord{
								Component:  ct.Metadata.Name,
								ResourceID: res.ID + "/forEach",
								Expression: e.InnerExpr,
								Error:      err,
							})
							continue
						}
						records = append(records, expressionCostRecord{
							Component:   ct.Metadata.Name,
							ResourceID:  res.ID + "/forEach",
							Expression:  e.InnerExpr,
							MinCost:     costEst.Min,
							MaxCost:     costEst.Max,
							IsMaxUint64: costEst.Max == math.MaxUint64,
						})
					}

					extEnv, err := validator.ResourceEnvironment(res.ForEach, res.Var)
					if err != nil {
						t.Fatalf("failed to extend env with forEach in %s/%s: %v", ct.Metadata.Name, res.ID, err)
					}
					if extEnv != nil {
						resEnv = extEnv
					}
				}

				// Template body evaluated with resEnv
				var bodyExprs []string
				if res.Template != nil {
					collectExpressions(res.Template, &bodyExprs)
				}
				for _, expr := range bodyExprs {
					totalExtracted++
					ast, issues := resEnv.Compile(expr)
					if issues != nil && issues.Err() != nil {
						skipped = append(skipped, skippedExpressionRecord{
							Component:  ct.Metadata.Name,
							ResourceID: res.ID,
							Expression: expr,
							Error:      issues.Err(),
						})
						continue
					}

					costEst, err := checker.Cost(ast.NativeRep(), coster)
					if err != nil {
						skipped = append(skipped, skippedExpressionRecord{
							Component:  ct.Metadata.Name,
							ResourceID: res.ID,
							Expression: expr,
							Error:      err,
						})
						continue
					}

					rec := expressionCostRecord{
						Component:   ct.Metadata.Name,
						ResourceID:  res.ID,
						Expression:  expr,
						MinCost:     costEst.Min,
						MaxCost:     costEst.Max,
						IsMaxUint64: costEst.Max == math.MaxUint64,
					}
					records = append(records, rec)
				}
			}
		}
	}

	t.Logf("TOTAL EXTRACTED EXPRESSIONS: %d", totalExtracted)
	t.Logf("TOTAL EVALUATED EXPRESSIONS: %d", len(records))
	t.Logf("TOTAL SKIPPED EXPRESSIONS:   %d", len(skipped))

	if len(skipped) > 0 {
		t.Log("=== SKIPPED EXPRESSIONS ===")
		for i, s := range skipped {
			t.Logf("  [%d] [%s/%s] %s (ERR: %v)", i+1, s.Component, s.ResourceID, s.Expression, s.Error)
		}
		t.Fatalf("FAIL: harness skipped %d expressions", len(skipped))
	}

	// Breakdown of finite vs MaxUint64
	var countUnder100, countUnder1k, countUnder10k, countUnder100k, countOver100kFinite, countMaxUint64 int
	for _, r := range records {
		if r.IsMaxUint64 {
			countMaxUint64++
			continue
		}
		switch {
		case r.MaxCost <= 100:
			countUnder100++
		case r.MaxCost <= 1000:
			countUnder1k++
		case r.MaxCost <= 10000:
			countUnder10k++
		case r.MaxCost <= 100000:
			countUnder100k++
		default:
			countOver100kFinite++
		}
	}

	t.Logf("=== PRODUCTION CEL COST DISTRIBUTION ACROSS CORPUS (%d expressions) ===", len(records))
	t.Logf("  Cost <= 100:             %d (%.1f%%)", countUnder100, float64(countUnder100)*100/float64(len(records)))
	t.Logf("  Cost 101 .. 1,000:       %d (%.1f%%)", countUnder1k, float64(countUnder1k)*100/float64(len(records)))
	t.Logf("  Cost 1,001 .. 10,000:    %d (%.1f%%)", countUnder10k, float64(countUnder10k)*100/float64(len(records)))
	t.Logf("  Cost 10,001 .. 100,000:  %d (%.1f%%)", countUnder100k, float64(countUnder100k)*100/float64(len(records)))
	t.Logf("  Cost > 100k (Finite):    %d (%.1f%%)", countOver100kFinite, float64(countOver100kFinite)*100/float64(len(records)))
	t.Logf("  Cost = MaxUint64 (Sat):  %d (%.1f%%)", countMaxUint64, float64(countMaxUint64)*100/float64(len(records)))

	t.Logf("=== DETAILED BREAKDOWN OF MaxUint64 (SATURATED / UNBOUNDED) EXPRESSIONS (%d) ===", countMaxUint64)
	for _, r := range records {
		if r.IsMaxUint64 {
			t.Logf("  [SATURATED] [%s/%s] %s", r.Component, r.ResourceID, r.Expression)
		}
	}

	t.Logf("=== DETAILED BREAKDOWN OF EXPENSIVE FINITE EXPRESSIONS (Cost > 1,000) ===")
	for _, r := range records {
		if !r.IsMaxUint64 && r.MaxCost > 1000 {
			t.Logf("  [%s/%s] Cost: %d | Expression: %s", r.Component, r.ResourceID, r.MaxCost, r.Expression)
		}
	}
}

func collectExpressions(data any, out *[]string) {
	switch v := data.(type) {
	case string:
		exprs, err := template.FindCELExpressions(v)
		if err == nil {
			for _, e := range exprs {
				*out = append(*out, e.InnerExpr)
			}
		}
	case map[string]any:
		for _, val := range v {
			collectExpressions(val, out)
		}
	case []any:
		for _, item := range v {
			collectExpressions(item, out)
		}
	}
}
