// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/checker"
	"gopkg.in/yaml.v3"
	apiservercel "k8s.io/apiserver/pkg/cel"
	"k8s.io/apimachinery/pkg/util/validation/field"

	"github.com/openchoreo/openchoreo/api/v1alpha1"
	"github.com/openchoreo/openchoreo/internal/template"
	"github.com/openchoreo/openchoreo/internal/validation/component"
	"github.com/openchoreo/openchoreo/internal/validation/component/cost"
	"github.com/openchoreo/openchoreo/internal/validation/schemautil"
)

type constructCase struct {
	Name       string
	Expression string
	SetupEnv   func(env *cel.Env, n int) (*cel.Env, *apiservercel.DeclType, map[string]any)
}

func main() {
	mode := flag.String("mode", "differential", "Report mode: differential, corpus, or lint")
	format := flag.String("format", "text", "Output format: text or json")
	policyBounds := flag.Bool("policy-bounds", false, "Enable policy bounds evaluation")
	flag.Parse()

	switch *mode {
	case "differential":
		runDifferentialReport()
	case "corpus", "lint":
		files := flag.Args()
		if len(files) == 0 && *mode == "corpus" {
			files = []string{"samples"}
		}
		if len(files) == 0 {
			fmt.Fprintln(os.Stderr, "Usage: costreport -mode lint [options] <files...>")
			os.Exit(3)
		}
		runLintReport(files, *format, *policyBounds)
	default:
		fmt.Fprintf(os.Stderr, "unsupported mode: %s (supported: differential, corpus, lint)\n", *mode)
		os.Exit(1)
	}
}

func runDifferentialReport() {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "CONSTRUCT\tEXPRESSION\tN\tSTATIC\tRUNTIME\tDELTA\tVERDICT\tNODE-LEVEL RULE")
	fmt.Fprintln(w, "---------\t----------\t-\t------\t-------\t-----\t-------\t---------------")

	cases := getConstructCases()
	ns := []int{0, 1, 100, 1000}

	for _, c := range cases {
		for _, n := range ns {
			baseEnv, err := cel.NewEnv(template.BaseCELExtensions()...)
			if err != nil {
				fmt.Fprintf(os.Stderr, "failed to create base cel env: %v\n", err)
				os.Exit(1)
			}

			env, decl, input := c.SetupEnv(baseEnv, n)

			// 1. Static Cost Estimation
			ast, issues := env.Compile(c.Expression)
			if issues != nil && issues.Err() != nil {
				fmt.Fprintf(w, "%s\t%s\t%d\tERR\tERR\tERR\tERROR\tCompile: %v\n", c.Name, c.Expression, n, issues.Err())
				continue
			}

			roots := make(map[string]*apiservercel.DeclType)
			if decl != nil {
				roots["items"] = decl
			}
			coster := cost.NewDeclTypeCostEstimator(roots)
			staticEst, err := checker.Cost(ast.NativeRep(), coster)
			if err != nil {
				fmt.Fprintf(w, "%s\t%s\t%d\tERR\tERR\tERR\tERROR\tCost: %v\n", c.Name, c.Expression, n, err)
				continue
			}

			// 2. Runtime Cost Metering
			progOpts := append([]cel.ProgramOption{cel.CostLimit(10_000_000)}, template.CustomFunctionCostOptions()...)
			prg, err := env.Program(ast, progOpts...)
			if err != nil {
				fmt.Fprintf(w, "%s\t%s\t%d\t%d\tERR\tERR\tERROR\tProgram: %v\n", c.Name, c.Expression, n, staticEst.Max, err)
				continue
			}

			val, evalDetails, err := prg.Eval(input)
			if err != nil || evalDetails == nil || evalDetails.ActualCost() == nil {
				// Eval failed
				fmt.Fprintf(w, "%s\t%s\t%d\t%d\tERR\tERR\tERROR\tEval: %v\n", c.Name, c.Expression, n, staticEst.Max, err)
				continue
			}
			_ = val
			runtimeCost := evalDetails.ActualCost()

			var deltaStr string
			verdict := "PASS"
			nodeRule := "static >= runtime"

			if staticEst.Max == math.MaxUint64 {
				deltaStr = "+Inf"
				verdict = "PASS"
				nodeRule = "UNKNOWN: unbounded / saturated input"
			} else {
				delta := int64(staticEst.Max) - int64(*runtimeCost)
				deltaStr = fmt.Sprintf("%+d", delta)
				if delta < 0 {
					verdict = "FAIL"
					nodeRule = "UNDERCALL: cause not found yet (open defect)"
				}
			}

			fmt.Fprintf(w, "%s\t%s\t%d\t%s\t%d\t%s\t%s\t%s\n",
				c.Name, truncate(c.Expression, 40), n, formatStatic(staticEst.Max), *runtimeCost, deltaStr, verdict, nodeRule)
		}
	}

	w.Flush()
}

func formatStatic(val uint64) string {
	if val == math.MaxUint64 {
		return "UNKNOWN"
	}
	return fmt.Sprintf("%d", val)
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}

func getConstructCases() []constructCase {
	return []constructCase{
		{
			Name:       "list literal in loop body",
			Expression: "items.map(x, [x])",
			SetupEnv: func(base *cel.Env, n int) (*cel.Env, *apiservercel.DeclType, map[string]any) {
				itemsDecl := apiservercel.NewListType(apiservercel.StringType, int64(n))
				env, _ := base.Extend(cel.Variable("items", cel.ListType(cel.StringType)))
				slice := make([]string, n)
				for i := 0; i < n; i++ {
					slice[i] = fmt.Sprintf("val-%d", i)
				}
				return env, itemsDecl, map[string]any{"items": slice}
			},
		},
		{
			Name:       "string concat",
			Expression: "items.map(x, x + '-suffix')",
			SetupEnv: func(base *cel.Env, n int) (*cel.Env, *apiservercel.DeclType, map[string]any) {
				itemsDecl := apiservercel.NewListType(apiservercel.StringType, int64(n))
				env, _ := base.Extend(cel.Variable("items", cel.ListType(cel.StringType)))
				slice := make([]string, n)
				for i := 0; i < n; i++ {
					slice[i] = fmt.Sprintf("val-%d", i)
				}
				return env, itemsDecl, map[string]any{"items": slice}
			},
		},
		{
			Name:       "in over list",
			Expression: "'target' in items",
			SetupEnv: func(base *cel.Env, n int) (*cel.Env, *apiservercel.DeclType, map[string]any) {
				itemsDecl := apiservercel.NewListType(apiservercel.StringType, int64(n))
				env, _ := base.Extend(cel.Variable("items", cel.ListType(cel.StringType)))
				slice := make([]string, n)
				for i := 0; i < n; i++ {
					slice[i] = fmt.Sprintf("val-%d", i)
				}
				if n > 0 {
					slice[n-1] = "target" // Worst case: match at the very end
				}
				return env, itemsDecl, map[string]any{"items": slice}
			},
		},
		{
			Name:       "filter comprehension",
			Expression: "items.filter(x, x != 'none')",
			SetupEnv: func(base *cel.Env, n int) (*cel.Env, *apiservercel.DeclType, map[string]any) {
				itemsDecl := apiservercel.NewListType(apiservercel.StringType, int64(n))
				env, _ := base.Extend(cel.Variable("items", cel.ListType(cel.StringType)))
				slice := make([]string, n)
				for i := 0; i < n; i++ {
					slice[i] = fmt.Sprintf("val-%d", i)
				}
				return env, itemsDecl, map[string]any{"items": slice}
			},
		},
		{
			Name:       "custom function oc_hash",
			Expression: "oc_hash(items[0])",
			SetupEnv: func(base *cel.Env, n int) (*cel.Env, *apiservercel.DeclType, map[string]any) {
				itemsDecl := apiservercel.NewListType(apiservercel.StringType, int64(n+1))
				env, _ := base.Extend(cel.Variable("items", cel.ListType(cel.StringType)))
				// Generate worst-case string with n characters
				strLen := n
				if strLen == 0 {
					strLen = 1
				}
				bytes := make([]byte, strLen)
				for i := range bytes {
					bytes[i] = 'a'
				}
				return env, itemsDecl, map[string]any{"items": []string{string(bytes)}}
			},
		},
		{
			Name:       "transformList + flatten (corpus defect)",
			Expression: "items.map(name, [name]).flatten()",
			SetupEnv: func(base *cel.Env, n int) (*cel.Env, *apiservercel.DeclType, map[string]any) {
				itemsDecl := apiservercel.NewListType(apiservercel.StringType, int64(n))
				env, _ := base.Extend(cel.Variable("items", cel.ListType(cel.StringType)))
				slice := make([]string, n)
				for i := 0; i < n; i++ {
					slice[i] = fmt.Sprintf("val-%d", i)
				}
				return env, itemsDecl, map[string]any{"items": slice}
			},
		},
	}
}

type lintSummary struct {
	Within int `json:"within"`
	Over   int `json:"over"`
	Unknown int `json:"unknown"`
	Errors int `json:"errors"`
}

type lintReportOutput struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	TotalCost  uint64 `json:"totalCost"`
	Verdict    string `json:"verdict"`
	Summary    lintSummary `json:"summary"`
	Resources  []cost.ResourceEstimate `json:"resources"`
}

func runLintReport(paths []string, format string, policyBounds bool) {
	var yamlFiles []string
	for _, p := range paths {
		stat, err := os.Stat(p)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error stating %s: %v\n", p, err)
			continue
		}
		if stat.IsDir() {
			_ = filepath.Walk(p, func(subPath string, info os.FileInfo, err error) error {
				if err == nil && !info.IsDir() && (strings.HasSuffix(subPath, ".yaml") || strings.HasSuffix(subPath, ".yml")) {
					yamlFiles = append(yamlFiles, subPath)
				}
				return nil
			})
		} else {
			yamlFiles = append(yamlFiles, p)
		}
	}

	exitCode := 0
	for _, file := range yamlFiles {
		content, err := os.ReadFile(file)
		if err != nil {
			continue
		}

		docs := strings.Split(string(content), "\n---")
		for _, doc := range docs {
			var meta struct {
				APIVersion string `yaml:"apiVersion"`
				Kind       string `yaml:"kind"`
				Metadata   struct {
					Name string `yaml:"name"`
				} `yaml:"metadata"`
			}
			if err := yaml.Unmarshal([]byte(doc), &meta); err != nil {
				continue
			}

			var ctSpec v1alpha1.ComponentTypeSpec
			switch meta.Kind {
			case "ComponentType":
				var ct v1alpha1.ComponentType
				if err := yaml.Unmarshal([]byte(doc), &ct); err != nil {
					continue
				}
				ctSpec = ct.Spec
			case "ClusterComponentType":
				var cct v1alpha1.ClusterComponentType
				if err := yaml.Unmarshal([]byte(doc), &cct); err != nil {
					continue
				}
				ctSpec = cct.Spec.ToComponentTypeSpec()
			default:
				continue
			}

			paramSchema, envConfigSchema, _ := schemautil.ExtractAndValidateSchemas(
				ctSpec.Parameters, ctSpec.EnvironmentConfigs, field.NewPath("spec"),
			)

			val, err := component.NewCELValidator(component.ComponentTypeResource, component.SchemaOptions{
				ParametersSchema:         paramSchema,
				EnvironmentConfigsSchema: envConfigSchema,
			})
			if err != nil {
				fmt.Fprintf(os.Stderr, "failed to create validator for %s: %v\n", meta.Metadata.Name, err)
				exitCode = 3
				continue
			}

			aggregator := cost.NewTemplateAggregator(val)
			tEst, err := aggregator.EstimateComponentTypeSpec(ctSpec)
			if err != nil {
				fmt.Fprintf(os.Stderr, "failed to estimate %s: %v\n", meta.Metadata.Name, err)
				exitCode = 3
				continue
			}

			var summary lintSummary
			for _, r := range tEst.Resources {
				switch r.Verdict {
				case cost.VerdictWithinBudget:
					summary.Within++
				case cost.VerdictOverBudget:
					summary.Over++
				case cost.VerdictUnknown:
					summary.Unknown++
				}
			}

			if tEst.Verdict == cost.VerdictOverBudget && exitCode < 1 {
				exitCode = 1
			} else if tEst.Verdict == cost.VerdictUnknown && exitCode < 2 {
				exitCode = 2
			}

			if format == "json" {
				report := lintReportOutput{
					APIVersion: meta.APIVersion,
					Kind:       meta.Kind,
					Name:       meta.Metadata.Name,
					TotalCost:  tEst.TotalCost,
					Verdict:    string(tEst.Verdict),
					Summary:    summary,
					Resources:  tEst.Resources,
				}
				data, _ := json.MarshalIndent(report, "", "  ")
				fmt.Println(string(data))
			} else {
				fmt.Printf("[%s] %s/%s: Verdict=%s TotalCost=%d (within: %d, over: %d, unknown: %d)\n",
					file, meta.Kind, meta.Metadata.Name, tEst.Verdict, tEst.TotalCost, summary.Within, summary.Over, summary.Unknown)
			}
		}
	}

	if exitCode != 0 {
		os.Exit(exitCode)
	}
}
