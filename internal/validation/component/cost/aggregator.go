// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package cost

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/checker"
	celast "github.com/google/cel-go/common/ast"
	"github.com/google/cel-go/common/types"
	apiservercel "k8s.io/apiserver/pkg/cel"

	"github.com/openchoreo/openchoreo/api/v1alpha1"
	"github.com/openchoreo/openchoreo/internal/template"
	"github.com/openchoreo/openchoreo/internal/validation/component"
)

// Runtime render and loop budget constants per Project 701 §2 & §4.4.
const (
	MaxCumulativeRenderBudget uint64 = 2_000_000
	MaxForEachItemsPerLoop    uint64 = 100
	MaxForEachItemsPerRender  uint64 = 10_000
	InterpolationBaseCharge   uint64 = 1
)

// Verdict denotes the overall admission verdict per Project 701 §4.5.
type Verdict string

const (
	VerdictWithinBudget Verdict = "WITHIN BUDGET"
	VerdictOverBudget   Verdict = "OVER BUDGET"
	VerdictUnknown      Verdict = "UNKNOWN"
)

// ExpressionEstimate holds the static estimate for a single CEL expression.
type ExpressionEstimate struct {
	FieldPath  string
	Expression string
	Cost       uint64
	State      SizeState
	Verdict    Verdict
}

// ResourceEstimate contains the aggregated cost estimate for a single ResourceTemplate.
type ResourceEstimate struct {
	ID                 string
	ForEachExpression  string
	LoopVar            string
	LoopBound          uint64
	IncludeWhenCost    uint64
	BodyCostPerIter    uint64
	TotalCost          uint64
	State              SizeState
	Verdict            Verdict
	Expressions        []ExpressionEstimate
	ForEachCapExceeded bool
}

// TemplateEstimate contains the aggregate cost analysis for an entire ComponentType or Trait.
type TemplateEstimate struct {
	TotalCost            uint64
	State                SizeState
	Verdict              Verdict
	Resources            []ResourceEstimate
	ValidationCost       uint64
	TotalForEachExpansions uint64
	ForEachRenderCapBreach bool
}

// TemplateAggregator computes worst-case aggregate costs for template specs per Project 701 §4.4 (B4).
type TemplateAggregator struct {
	validator *component.CELValidator
	coster    *DeclTypeCostEstimator
}

// NewTemplateAggregator creates an aggregator using the single environment factory.
func NewTemplateAggregator(val *component.CELValidator) *TemplateAggregator {
	var roots map[string]*apiservercel.DeclType
	if val != nil {
		roots = val.GetRootTypes()
	}
	return &TemplateAggregator{
		validator: val,
		coster:    NewDeclTypeCostEstimator(roots),
	}
}

// EstimateExpression estimates a single CEL expression in a given environment.
func (a *TemplateAggregator) EstimateExpression(expr string, env *cel.Env, fieldPath string) ExpressionEstimate {
	ast, issues := env.Compile(expr)
	if issues != nil && issues.Err() != nil {
		return ExpressionEstimate{
			FieldPath:  fieldPath,
			Expression: expr,
			Cost:       math.MaxUint64,
			State:      StateUnknown,
			Verdict:    VerdictUnknown,
		}
	}

	est, err := checker.Cost(ast.NativeRep(), a.coster)
	if err != nil {
		return ExpressionEstimate{
			FieldPath:  fieldPath,
			Expression: expr,
			Cost:       math.MaxUint64,
			State:      StateUnknown,
			Verdict:    VerdictUnknown,
		}
	}

	state := StateKnown
	costVal := est.Max
	if costVal >= (1 << 63) || costVal == math.MaxUint64 {
		state = StateUnknown
		costVal = math.MaxUint64
	}

	verdict := VerdictWithinBudget
	if state == StateUnknown {
		verdict = VerdictUnknown
	} else if costVal > MaxCumulativeRenderBudget {
		verdict = VerdictOverBudget
	}

	return ExpressionEstimate{
		FieldPath:  fieldPath,
		Expression: expr,
		Cost:       costVal,
		State:      state,
		Verdict:    verdict,
	}
}

// EstimateResource computes the worst-case cost for a single resource template per Project 701 §4.4.
func (a *TemplateAggregator) EstimateResource(res v1alpha1.ResourceTemplate) (ResourceEstimate, error) {
	out := ResourceEstimate{
		ID:    res.ID,
		State: StateKnown,
	}

	baseEnv := a.validator.GetBaseEnv()

	// 1. includeWhen evaluated in base environment (worst case: taken as true)
	if res.IncludeWhen != "" {
		inner := unwrapCEL(res.IncludeWhen)
		est := a.EstimateExpression(inner, baseEnv, fmt.Sprintf("resources[%s].includeWhen", res.ID))
		out.Expressions = append(out.Expressions, est)
		out.IncludeWhenCost = est.Cost
		if est.State == StateUnknown {
			out.State = StateUnknown
		}
	}

	// 2. forEach expression evaluation & loop bound calculation
	loopBound := uint64(1)
	loopVarEnv := baseEnv

	if res.ForEach != "" {
		inner := unwrapCEL(res.ForEach)
		out.ForEachExpression = inner
		out.LoopVar = res.Var

		// Estimate forEach collection expression itself
		forEachEst := a.EstimateExpression(inner, baseEnv, fmt.Sprintf("resources[%s].forEach", res.ID))
		out.Expressions = append(out.Expressions, forEachEst)
		if forEachEst.State == StateUnknown {
			out.State = StateUnknown
		}

		// Calculate loop size M = min(size(collection), 100)
		ast, _ := baseEnv.Compile(inner)
		if ast != nil {
			var path []string
			if innerTrimmed := strings.TrimSpace(inner); innerTrimmed != "" {
				path = strings.Split(innerTrimmed, ".")
			}
			sizeEst := a.coster.EstimateSize(pathAstNode{path: path})
			if sizeEst != nil && sizeEst.Max > 0 && sizeEst.Max < math.MaxUint64 {
				if sizeEst.Max > MaxForEachItemsPerLoop {
					loopBound = MaxForEachItemsPerLoop
					out.ForEachCapExceeded = true
				} else {
					loopBound = sizeEst.Max
				}
			} else {
				// Unsizable collection defaults to cap of 100 but marks state as UNKNOWN
				loopBound = MaxForEachItemsPerLoop
				out.State = StateUnknown
			}
		} else {
			loopBound = MaxForEachItemsPerLoop
			out.State = StateUnknown
		}

		// Create extended resource environment for loop body
		extEnv, err := a.validator.ResourceEnvironment(res.ForEach, res.Var)
		if err != nil {
			return out, fmt.Errorf("failed to get resource environment for %s: %w", res.ID, err)
		}
		loopVarEnv = extEnv
	}
	out.LoopBound = loopBound

	// 3. Extract and cost all CEL expressions in the resource body
	var bodyExpressions []ExpressionEstimate
	if res.Template != nil && len(res.Template.Raw) > 0 {
		var data any
		if err := json.Unmarshal(res.Template.Raw, &data); err == nil {
			a.walkExpressions(data, fmt.Sprintf("resources[%s].template", res.ID), loopVarEnv, &bodyExpressions)
		}
	}

	// 4. Aggregate body costs
	var bodyCostPerIter uint64
	for _, be := range bodyExpressions {
		out.Expressions = append(out.Expressions, be)
		if be.State == StateUnknown {
			out.State = StateUnknown
		}
		bodyCostPerIter = satAdd(bodyCostPerIter, be.Cost)
	}
	out.BodyCostPerIter = bodyCostPerIter

	// 5. Total cost = includeWhen + forEach.expr + M * bodyCostPerIter
	total := out.IncludeWhenCost
	if res.ForEach != "" && len(out.Expressions) > 0 {
		// Include the forEach expression cost (already in out.Expressions)
		total = satAdd(total, out.Expressions[1].Cost)
	}

	scaledBody := saturatingMul(loopBound, bodyCostPerIter)
	total = satAdd(total, scaledBody)
	out.TotalCost = total

	if out.State == StateUnknown || total >= (1<<63) || total == math.MaxUint64 {
		out.State = StateUnknown
		out.TotalCost = math.MaxUint64
		out.Verdict = VerdictUnknown
	} else if out.TotalCost > MaxCumulativeRenderBudget {
		out.Verdict = VerdictOverBudget
	} else {
		out.Verdict = VerdictWithinBudget
	}

	return out, nil
}

// EstimateComponentType aggregates all resources and validation rules for a ComponentType per Project 701 §4.4.
func (a *TemplateAggregator) EstimateComponentType(ct *v1alpha1.ComponentType) (TemplateEstimate, error) {
	return a.EstimateComponentTypeSpec(ct.Spec)
}

// EstimateComponentTypeSpec aggregates all resources and validation rules for a ComponentTypeSpec per Project 701 §4.4.
func (a *TemplateAggregator) EstimateComponentTypeSpec(spec v1alpha1.ComponentTypeSpec) (TemplateEstimate, error) {
	out := TemplateEstimate{
		State: StateKnown,
	}

	baseEnv := a.validator.GetBaseEnv()

	// 1. Cost validations & preRenderValidations
	var valCost uint64
	allValidations := append([]v1alpha1.ValidationRule(nil), spec.Validations...)
	allValidations = append(allValidations, spec.PreRenderValidations...)
	for i, rule := range allValidations {
		if rule.Rule != "" {
			inner := unwrapCEL(rule.Rule)
			est := a.EstimateExpression(inner, baseEnv, fmt.Sprintf("validations[%d]", i))
			valCost = satAdd(valCost, est.Cost)
			if est.State == StateUnknown {
				out.State = StateUnknown
			}
		}
	}
	out.ValidationCost = valCost

	// 2. Cost resources
	var resourcesTotal uint64
	var totalExpansions uint64
	for _, res := range spec.Resources {
		rEst, err := a.EstimateResource(res)
		if err != nil {
			return out, err
		}
		out.Resources = append(out.Resources, rEst)
		if rEst.State == StateUnknown {
			out.State = StateUnknown
		}
		resourcesTotal = satAdd(resourcesTotal, rEst.TotalCost)
		totalExpansions = satAdd(totalExpansions, rEst.LoopBound)
	}

	out.TotalForEachExpansions = totalExpansions
	if totalExpansions > MaxForEachItemsPerRender {
		out.ForEachRenderCapBreach = true
		out.State = StateUnknown
	}

	total := satAdd(valCost, resourcesTotal)
	out.TotalCost = total

	if out.State == StateUnknown || total >= (1<<63) || total == math.MaxUint64 {
		out.State = StateUnknown
		out.TotalCost = math.MaxUint64
		out.Verdict = VerdictUnknown
	} else if out.TotalCost > MaxCumulativeRenderBudget {
		out.Verdict = VerdictOverBudget
	} else {
		out.Verdict = VerdictWithinBudget
	}

	return out, nil
}

// walkExpressions recursively locates and estimates CEL expressions inside template JSON.
func (a *TemplateAggregator) walkExpressions(data any, path string, env *cel.Env, out *[]ExpressionEstimate) {
	switch v := data.(type) {
	case string:
		matches, err := template.FindCELExpressions(v)
		if err != nil || len(matches) == 0 {
			return
		}

		if len(matches) == 1 && strings.TrimSpace(v) == matches[0].FullExpr {
			// Standalone expression
			est := a.EstimateExpression(matches[0].InnerExpr, env, path)
			*out = append(*out, est)
			return
		}

		// Interpolation expression: k + sum(cost(e_i)) per Project 701 §4.4
		var sum uint64 = uint64(len(matches)) * InterpolationBaseCharge
		state := StateKnown
		for _, m := range matches {
			est := a.EstimateExpression(m.InnerExpr, env, path)
			sum = satAdd(sum, est.Cost)
			if est.State == StateUnknown {
				state = StateUnknown
			}
		}
		verdict := VerdictWithinBudget
		if state == StateUnknown {
			verdict = VerdictUnknown
		} else if sum > MaxCumulativeRenderBudget {
			verdict = VerdictOverBudget
		}
		*out = append(*out, ExpressionEstimate{
			FieldPath:  path,
			Expression: v,
			Cost:       sum,
			State:      state,
			Verdict:    verdict,
		})

	case map[string]any:
		for k, val := range v {
			childPath := fmt.Sprintf("%s.%s", path, k)
			if strings.Contains(k, "${") {
				matches, err := template.FindCELExpressions(k)
				if err == nil {
					for _, m := range matches {
						est := a.EstimateExpression(m.InnerExpr, env, fmt.Sprintf("%s[key]", path))
						*out = append(*out, est)
					}
				}
			}
			a.walkExpressions(val, childPath, env, out)
		}

	case []any:
		for i, elem := range v {
			childPath := fmt.Sprintf("%s[%d]", path, i)
			a.walkExpressions(elem, childPath, env, out)
		}
	}
}

func unwrapCEL(s string) string {
	trimmed := strings.TrimSpace(s)
	if strings.HasPrefix(trimmed, "${") && strings.HasSuffix(trimmed, "}") {
		return strings.TrimSpace(trimmed[2 : len(trimmed)-1])
	}
	return trimmed
}

func satAdd(a, b uint64) uint64 {
	if a > math.MaxUint64-b {
		return math.MaxUint64
	}
	return a + b
}

func saturatingMul(a, b uint64) uint64 {
	if a == 0 || b == 0 {
		return 0
	}
	if a > math.MaxUint64/b {
		return math.MaxUint64
	}
	return a * b
}

type pathAstNode struct {
	path []string
}

func (p pathAstNode) Path() []string {
	return p.path
}

func (p pathAstNode) Type() *types.Type {
	return nil
}

func (p pathAstNode) Expr() celast.Expr {
	return nil
}

func (p pathAstNode) ComputedSize() *checker.SizeEstimate {
	return nil
}

