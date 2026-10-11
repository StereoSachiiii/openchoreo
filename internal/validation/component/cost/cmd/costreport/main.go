// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"flag"
	"fmt"
	"math"
	"os"
	"text/tabwriter"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/checker"
	apiservercel "k8s.io/apiserver/pkg/cel"

	"github.com/openchoreo/openchoreo/internal/template"
	"github.com/openchoreo/openchoreo/internal/validation/component/cost"
)

type constructCase struct {
	Name       string
	Expression string
	SetupEnv   func(env *cel.Env, n int) (*cel.Env, *apiservercel.DeclType, map[string]any)
}

func main() {
	mode := flag.String("mode", "differential", "Report mode: differential or corpus")
	flag.Parse()

	if *mode != "differential" {
		fmt.Fprintf(os.Stderr, "unsupported mode: %s (supported: differential)\n", *mode)
		os.Exit(1)
	}

	runDifferentialReport()
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
