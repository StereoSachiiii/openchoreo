// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package cost_test

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/checker"
	apiextschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	apiservercel "k8s.io/apiserver/pkg/cel"
	"sigs.k8s.io/yaml"

	"github.com/openchoreo/openchoreo/api/v1alpha1"
	"github.com/openchoreo/openchoreo/internal/pipeline/component/context"
	ocschema "github.com/openchoreo/openchoreo/internal/schema"
	"github.com/openchoreo/openchoreo/internal/template"
	"github.com/openchoreo/openchoreo/internal/validation/component"
	"github.com/openchoreo/openchoreo/internal/validation/component/cost"
	"github.com/openchoreo/openchoreo/internal/validation/component/decltype"
)

func TestE0b_DetailedResolution(t *testing.T) {
	t.Log("=== E0b QUESTION 1: SAMPLE EXPRESSION IN component-with-configs.yaml:180 ===")

	filePath := "../../../../samples/component-types/component-with-configs/component-with-configs.yaml"
	absPath, err := filepath.Abs(filePath)
	if err != nil {
		t.Fatalf("failed to resolve path: %v", err)
	}

	content, err := os.ReadFile(absPath)
	if err != nil {
		t.Fatalf("failed to read file: %v", err)
	}

	var ct v1alpha1.ClusterComponentType
	if err := yaml.Unmarshal(content, &ct); err != nil {
		t.Fatalf("failed to unmarshal YAML: %v", err)
	}

	// 1.a Declared maxItems for envConfig.envs
	t.Log("--- 1.a: Declared maxItems for envConfig.envs ---")
	var envConfigStruct *apiextschema.Structural
	if ct.Spec.EnvironmentConfigs != nil {
		envConfigStruct, err = ocschema.ResolveSectionToStructural(ct.Spec.EnvironmentConfigs)
		if err != nil {
			t.Fatalf("failed to resolve section: %v", err)
		}
	}

	if envConfigStruct == nil {
		t.Log("environmentConfigs schema is nil")
	} else {
		b, _ := json.Marshal(envConfigStruct)
		t.Logf("environmentConfigs Structural: %s", string(b))
	}
	t.Log("ANALYSIS of 1.a: envConfig in '${envConfig.envs.transformMapEntry(...)}' is NOT a property of environmentConfigs! It is the loop variable defined by forEach: '${configurations.toConfigEnvsByContainer()}' with var: 'envConfig'. The schema for configurations is defined by the Go type context.ConfigurationsContext. Does context.ConfigurationsContext or Configurations declare a maxItems? In context.ComponentContext, configurations is reflected from context.ConfigurationsContext. In DeclType, toConfigEnvsByContainer() is a custom method/macro. It has NO OpenAPI schema bound declared (unbounded, fallback 1000 or 100).")

	// 1.b Check presence in corpus harness (336 expressions)
	t.Log("--- 1.b: Static estimate from corpus harness (presence in 336) ---")
	validator, err := component.NewCELValidator(component.ComponentTypeResource, component.SchemaOptions{
		EnvironmentConfigsSchema: envConfigStruct,
	})
	if err != nil {
		t.Fatalf("failed to create validator: %v", err)
	}
	baseEnv := validator.GetBaseEnv()
	targetExpr := "envConfig.envs.transformMapEntry(index, env, {env.name: env.value})"
	_, issues := baseEnv.Compile(targetExpr)
	if issues != nil && issues.Err() != nil {
		t.Logf("Compile error against baseEnv: %v", issues.Err())
		t.Log("RAW REASON FOR ABSENCE FROM 336: In cost_distribution_test.go, all resource template expressions were compiled using baseEnv directly without registering the forEach variable 'envConfig' into the environment. As a result, compiling 'envConfig.envs...' produced undeclared reference 'envConfig', and the test skipped it on line 221 (continue).")
	}

	// 1.c Static estimate with properly constructed environment
	t.Log("--- 1.c: Static estimate with properly constructed environment ---")
	// Build the environment with envConfig registered
	schemaBasedFields := map[string]bool{"parameters": true, "environmentConfigs": true}
	reflectedFields := decltype.ExtractFields(reflect.TypeFor[context.ComponentContext](), schemaBasedFields)
	roots := make(map[string]*apiservercel.DeclType)
	for _, rf := range reflectedFields {
		roots[rf.Name] = rf.DeclType
	}

	// envConfig contains envs: []EnvEntry
	envEntryType := apiservercel.NewObjectType("EnvEntry", map[string]*apiservercel.DeclField{
		"name":  apiservercel.NewDeclField("name", apiservercel.StringType, false, nil, nil),
		"value": apiservercel.NewDeclField("value", apiservercel.StringType, false, nil, nil),
	})
	// Try with DeclType fallback (unbounded list)
	// In DeclTypeCostEstimator, if MaxElements <= 0, what does it return?
	envsListTypeUnbounded := apiservercel.NewListType(envEntryType, 0)
	roots["envConfig"] = apiservercel.NewObjectType("ConfigEnv", map[string]*apiservercel.DeclField{
		"resourceName": apiservercel.NewDeclField("resourceName", apiservercel.StringType, false, nil, nil),
		"envs":         apiservercel.NewDeclField("envs", envsListTypeUnbounded, false, nil, nil),
	})

	costerUnbounded := cost.NewDeclTypeCostEstimator(roots)
	opts := template.BaseCELExtensions()
	opts = append(opts, context.CELExtensions()...)
	var declList []*apiservercel.DeclType
	for name, dt := range roots {
		declList = append(declList, dt)
		opts = append(opts, cel.Variable(name, dt.CelType()))
	}
	provider := apiservercel.NewDeclTypeProvider(declList...)
	bEnv, err := cel.NewEnv(opts...)
	if err != nil {
		t.Fatalf("base env error: %v", err)
	}
	provOpts, err := provider.EnvOptions(bEnv.CELTypeProvider())
	if err != nil {
		t.Fatalf("provOpts error: %v", err)
	}
	fullEnv, err := bEnv.Extend(provOpts...)
	if err != nil {
		t.Fatalf("fullEnv error: %v", err)
	}

	astUnbounded, issues := fullEnv.Compile(targetExpr)
	if issues != nil && issues.Err() != nil {
		t.Fatalf("compile error: %v", issues.Err())
	}
	estUnbounded, err := checker.Cost(astUnbounded.NativeRep(), costerUnbounded)
	if err != nil {
		t.Fatalf("cost error: %v", err)
	}
	t.Logf("Static estimate when envConfig.envs has MaxElements=0 (unbounded): Min=%d, Max=%d", estUnbounded.Min, estUnbounded.Max)

	// Now try when envConfig.envs has MaxElements = 1000
	envsListType1000 := apiservercel.NewListType(envEntryType, 1000)
	roots["envConfig"] = apiservercel.NewObjectType("ConfigEnv", map[string]*apiservercel.DeclField{
		"resourceName": apiservercel.NewDeclField("resourceName", apiservercel.StringType, false, nil, nil),
		"envs":         apiservercel.NewDeclField("envs", envsListType1000, false, nil, nil),
	})
	coster1000 := cost.NewDeclTypeCostEstimator(roots)
	est1000, err := checker.Cost(astUnbounded.NativeRep(), coster1000)
	if err != nil {
		t.Fatalf("cost error: %v", err)
	}
	t.Logf("Static estimate when envConfig.envs has MaxElements=1000: Min=%d, Max=%d", est1000.Min, est1000.Max)

	// 1.d Runtime ActualCost at N = declared bound, 2x bound, and N=1000
	t.Log("--- 1.d: Runtime ActualCost measurements ---")
	progOpts := append([]cel.ProgramOption{
		cel.CostLimit(math.MaxUint64),
	}, template.CustomFunctionCostOptions()...)
	prog, err := fullEnv.Program(astUnbounded, progOpts...)
	if err != nil {
		t.Fatalf("program error: %v", err)
	}

	measureRuntime := func(n int) uint64 {
		envsList := make([]any, 0, n)
		for i := 0; i < n; i++ {
			envsList = append(envsList, map[string]any{
				"name":  fmt.Sprintf("VAR_%d", i),
				"value": fmt.Sprintf("VAL_%d", i),
			})
		}
		inputs := map[string]any{
			"envConfig": map[string]any{
				"envs": envsList,
			},
		}
		_, details, err := prog.Eval(inputs)
		if err != nil {
			t.Fatalf("eval error at N=%d: %v", n, err)
		}
		if details != nil && details.ActualCost() != nil {
			return *details.ActualCost()
		}
		return 0
	}

	costN100 := measureRuntime(100)
	costN200 := measureRuntime(200)
	costN1000 := measureRuntime(1000)
	t.Logf("Runtime ActualCost at N=100:  %d", costN100)
	t.Logf("Runtime ActualCost at N=200:  %d", costN200)
	t.Logf("Runtime ActualCost at N=1000: %d", costN1000)

	// 2. Exact source of the gap between static formula and runtime actual cost
	t.Log("--- 2: Exact source of cost terms in cel-go ---")
	t.Logf("At N=1000: Static formula gave 36,033, Runtime actual was %d", costN1000)
	// Let's test at N=0, N=1, N=2 to find the exact runtime equation
	costN0 := measureRuntime(0)
	costN1 := measureRuntime(1)
	costN2 := measureRuntime(2)
	t.Logf("Runtime ActualCost at N=0: %d", costN0)
	t.Logf("Runtime ActualCost at N=1: %d", costN1)
	t.Logf("Runtime ActualCost at N=2: %d", costN2)
	t.Logf("Runtime delta per element (N=2 - N=1): %d", costN2-costN1)
	t.Logf("Runtime delta per element (N=1 - N=0): %d", costN1-costN0)

	// 3. 33,294 expression sub-expression breakdown
	t.Log("--- 3: 33,294 expression per-subexpression static cost ---")
	expr33294 := `workload.endpoints.transformList(name, ep, ("external" in ep.visibility && ep.type in ["HTTP", "GraphQL", "Websocket"]) ? [name] : []).flatten()`
	ast33294, issues := fullEnv.Compile(expr33294)
	if issues != nil && issues.Err() != nil {
		t.Fatalf("compile error for 33294: %v", issues.Err())
	}

	// Print static costs for subexpressions
	subExprs := []string{
		`"external" in ep.visibility`,
		`ep.type in ["HTTP", "GraphQL", "Websocket"]`,
		`("external" in ep.visibility && ep.type in ["HTTP", "GraphQL", "Websocket"])`,
		`("external" in ep.visibility && ep.type in ["HTTP", "GraphQL", "Websocket"]) ? [name] : []`,
		`workload.endpoints`,
	}
	epType := apiservercel.NewObjectType("Endpoint", map[string]*apiservercel.DeclField{
		"type":       apiservercel.NewDeclField("type", apiservercel.StringType, false, nil, nil),
		"visibility": apiservercel.NewDeclField("visibility", apiservercel.NewListType(apiservercel.StringType, 0), false, nil, nil),
	})
	subRoots := map[string]*apiservercel.DeclType{
		"workload": roots["workload"],
		"ep":       epType,
		"name":     apiservercel.StringType,
	}
	subCoster := cost.NewDeclTypeCostEstimator(subRoots)
	epProvider := apiservercel.NewDeclTypeProvider(epType)
	epProvOpts, err := epProvider.EnvOptions(fullEnv.CELTypeProvider())
	if err != nil {
		t.Fatalf("epProvOpts error: %v", err)
	}
	subOpts := append(epProvOpts,
		cel.Variable("ep", epType.CelType()),
		cel.Variable("name", cel.StringType),
	)
	subEnv, err := fullEnv.Extend(subOpts...)
	if err != nil {
		t.Fatalf("subEnv error: %v", err)
	}
	for _, se := range subExprs {
		sAst, sIssues := subEnv.Compile(se)
		if sIssues != nil && sIssues.Err() != nil {
			t.Logf("Subexpression %q compile err: %v", se, sIssues.Err())
			continue
		}
		sCost, sErr := checker.Cost(sAst.NativeRep(), subCoster)
		if sErr != nil {
			t.Logf("Subexpression %q cost err: %v", se, sErr)
			continue
		}
		t.Logf("SUBEXPRESSION STATIC COST: %-85s => Min=%-5d Max=%-5d", se, sCost.Min, sCost.Max)
	}

	prog33294, err := fullEnv.Program(ast33294, progOpts...)
	if err != nil {
		t.Fatalf("program error for 33294: %v", err)
	}

	// Real worst-case input:
	// 32 endpoints (assumed/policy bound for workload.endpoints)
	// ep.visibility has N=1000 items (cel-go assumed fallback size for unbounded list)
	// "external" is placed at index 999 (the VERY LAST element, so no short-circuiting on 'in')
	// ep.type is "HTTP" (matches the whitelist, so the entire right-hand side of && evaluates and matches)
	// ternary condition is TRUE, so it materializes [name]
	realWorstInputs := map[string]any{
		"workload": map[string]any{
			"endpoints": func() map[string]any {
				m := make(map[string]any)
				for i := 0; i < 32; i++ {
					vis := make([]any, 0, 1000)
					for j := 0; j < 999; j++ {
						vis = append(vis, fmt.Sprintf("other_vis_%d", j))
					}
					vis = append(vis, "external") // placed last!
					m[fmt.Sprintf("ep%d", i)] = map[string]any{
						"type":       "HTTP",
						"visibility": vis,
					}
				}
				return m
			}(),
		},
	}
	_, detRealWorst, err := prog33294.Eval(realWorstInputs)
	if err != nil {
		t.Fatalf("eval real worst error: %v", err)
	}
	t.Logf("Runtime ActualCost for 33,294 @ REAL WORST-CASE (32 endpoints, 1000 visibilities, 'external' at index 999, type HTTP): %d", *detRealWorst.ActualCost())

	// Best-case input: 0 endpoints
	bestInputs := map[string]any{
		"workload": map[string]any{
			"endpoints": map[string]any{},
		},
	}
	_, detBest, err := prog33294.Eval(bestInputs)
	if err != nil {
		t.Fatalf("eval best error: %v", err)
	}
	t.Logf("Runtime ActualCost for 33,294 @ BEST-CASE (0 endpoints): %d", *detBest.ActualCost())
}

func TestCorpus343vs336Discrepancy(t *testing.T) {
	t.Log("=== E0b QUESTION 4: 343 vs 336 EXPRESSION AUDIT ===")

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

	totalFound := 0
	compiledOK := 0
	var skippedExprs []string

	schemaBasedFields := map[string]bool{"parameters": true, "environmentConfigs": true}
	reflectedFields := decltype.ExtractFields(reflect.TypeFor[context.ComponentContext](), schemaBasedFields)

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

			roots := make(map[string]*apiservercel.DeclType)
			for _, rf := range reflectedFields {
				roots[rf.Name] = rf.DeclType
			}
			opts := template.BaseCELExtensions()
			opts = append(opts, context.CELExtensions()...)
			declTypesList := make([]*apiservercel.DeclType, 0, len(roots))
			for name, dt := range roots {
				if dt != nil {
					declTypesList = append(declTypesList, dt)
					opts = append(opts, cel.Variable(name, dt.CelType()))
				}
			}
			provider := apiservercel.NewDeclTypeProvider(declTypesList...)
			baseEnv, err := cel.NewEnv(opts...)
			if err != nil {
				continue
			}
			provOpts, err := provider.EnvOptions(baseEnv.CELTypeProvider())
			if err != nil {
				continue
			}
			env, err := baseEnv.Extend(provOpts...)
			if err != nil {
				continue
			}

			allRules := append(ct.Spec.Validations, ct.Spec.PreRenderValidations...)
			for _, r := range allRules {
				exprs, err := template.FindCELExpressions(r.Rule)
				if err == nil {
					for _, expr := range exprs {
						totalFound++
						ast, issues := env.Compile(expr.InnerExpr)
						if issues != nil && issues.Err() != nil {
							skippedExprs = append(skippedExprs, fmt.Sprintf("[%s validation] %s (ERR: %v)", ct.Metadata.Name, expr.InnerExpr, issues.Err()))
						} else if ast != nil {
							compiledOK++
						}
					}
				}
			}

			for _, res := range ct.Spec.Resources {
				var exprs []string
				if res.IncludeWhen != "" {
					if ex, err := template.FindCELExpressions(res.IncludeWhen); err == nil {
						for _, e := range ex {
							exprs = append(exprs, e.InnerExpr)
						}
					}
				}
				if res.ForEach != "" {
					if ex, err := template.FindCELExpressions(res.ForEach); err == nil {
						for _, e := range ex {
							exprs = append(exprs, e.InnerExpr)
						}
					}
				}
				if res.Template != nil {
					collectExpressions(res.Template, &exprs)
				}

				for _, expr := range exprs {
					totalFound++
					ast, issues := env.Compile(expr)
					if issues != nil && issues.Err() != nil {
						skippedExprs = append(skippedExprs, fmt.Sprintf("[%s/%s] %s (ERR: %v)", ct.Metadata.Name, res.ID, expr, issues.Err()))
					} else if ast != nil {
						compiledOK++
					}
				}
			}
		}
	}

	t.Logf("TOTAL EXPRESSIONS EXTRACTED FROM CORPUS: %d", totalFound)
	t.Logf("TOTAL COMPILED SUCCESSFULLY IN BASE ENV: %d", compiledOK)
	t.Logf("TOTAL SKIPPED DUE TO COMPILATION ERROR:   %d", len(skippedExprs))
	t.Log("--- SKIPPED EXPRESSIONS LIST ---")
	for i, s := range skippedExprs {
		t.Logf("  [%d] %s", i+1, s)
	}
}
