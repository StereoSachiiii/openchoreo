// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package cost

import (
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"slices"
	"strings"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/checker"
	"k8s.io/apimachinery/pkg/runtime"
	apiservercel "k8s.io/apiserver/pkg/cel"

	"github.com/openchoreo/openchoreo/internal/template"
)

// LoopInfo configures a forEach loop on a ResourceScope.
type LoopInfo struct {
	Bound       uint64
	CapExceeded bool
	State       SizeState
	Expr        *ExpressionEstimate
	BodyCoster  *DeclTypeCostEstimator
}

// SpecEstimator accumulates worst-case CEL compute cost across a template CR spec.
//
// It works with two ceilings (configured via Option):
//   - expressionLimit: applied to each individual CEL expression and each interpolated string.
//   - budget: applied to each resource total and the cumulative spec total returned by Result.
type SpecEstimator struct {
	coster          *DeclTypeCostEstimator
	expressionLimit uint64
	budget          uint64
	validationTally costTally
	resources       []*ResourceScope
}

// NewSpecEstimator creates an estimator for a CR spec using the given root DeclTypes and budget options.
// Without options it uses DefaultCELCostLimit and MaxCumulativeRenderBudget.
func NewSpecEstimator(roots map[string]*apiservercel.DeclType, opts ...Option) *SpecEstimator {
	cfg := resolveBudgetConfig(opts)
	return &SpecEstimator{
		coster:          NewDeclTypeCostEstimator(roots),
		expressionLimit: cfg.expressionLimit,
		budget:          cfg.budget,
		validationTally: newCostTally(),
	}
}

// Coster returns the underlying DeclTypeCostEstimator.
func (e *SpecEstimator) Coster() *DeclTypeCostEstimator {
	return e.coster
}

// EstimateExpr compiles and estimates a single CEL expression directly against env.
func (e *SpecEstimator) EstimateExpr(env *cel.Env, fieldPath, rawExpr string) ExpressionEstimate {
	return e.estimateExpr(env, e.coster, fieldPath, rawExpr)
}

// EstimateSize estimates the bounded size of a schema path using the root coster.
func (e *SpecEstimator) EstimateSize(path ...string) *checker.SizeEstimate {
	if e == nil || e.coster == nil {
		return nil
	}
	return e.coster.EstimateSize(NewPathAstNode(path...))
}

// AddValidation estimates a spec-level validation rule (spec.validations / spec.preRenderValidations).
func (e *SpecEstimator) AddValidation(env *cel.Env, fieldPath, rawExpr string) {
	if rawExpr == "" {
		return
	}
	e.validationTally.add(e.estimateExpr(env, e.coster, fieldPath, rawExpr))
}

// Resource starts a named resource/entry within the spec (default loopBound = 1).
func (e *SpecEstimator) Resource(id string) *ResourceScope {
	r := &ResourceScope{
		id:        id,
		est:       e,
		loopBound: 1,
		loopState: StateKnown,
		coster:    e.coster,
	}
	e.resources = append(e.resources, r)
	return r
}

// Result finalizes all validations and resources, enforces MaxForEachItemsPerRender, and applies the cumulative budget.
func (e *SpecEstimator) Result() TemplateEstimate {
	var (
		resEstimates    = make([]ResourceEstimate, 0, len(e.resources))
		resourcesTotal  uint64
		totalExpansions uint64
		hasOverBudget   = e.validationTally.anyOverBudget
		overallState    = e.validationTally.state
	)

	for _, r := range e.resources {
		rEst := r.build(e.budget)
		resEstimates = append(resEstimates, rEst)
		if rEst.State == StateUnknown {
			overallState = StateUnknown
		}
		if rEst.Verdict == VerdictOverBudget {
			hasOverBudget = true
		}
		resourcesTotal = SatAdd(resourcesTotal, rEst.TotalCost)
		totalExpansions = SatAdd(totalExpansions, rEst.LoopBound)
	}

	est := TemplateEstimate{
		Resources:              resEstimates,
		ValidationCost:         e.validationTally.cost,
		TotalForEachExpansions: totalExpansions,
		State:                  overallState,
	}

	if totalExpansions > MaxForEachItemsPerRender {
		est.ForEachRenderCapBreach = true
		est.State = StateUnknown
	}

	est.TotalCost = SatAdd(est.ValidationCost, resourcesTotal)
	est.State, est.Verdict = BudgetVerdict(est.TotalCost, est.State, e.budget, hasOverBudget)
	return est
}

// estimateExpr is the single atomic CEL expression coster: unwraps ${...}, compiles against env,
// and costs using coster. Expressions that fail to compile or cannot be costed are charged
// MaxObjectSizeBytes and reported as UNKNOWN.
func (e *SpecEstimator) estimateExpr(env *cel.Env, coster *DeclTypeCostEstimator, fieldPath, rawExpr string) ExpressionEstimate {
	expr := UnwrapCEL(rawExpr)
	if coster == nil {
		coster = e.coster
	}

	ast, issues := env.Compile(expr)
	if issues != nil && issues.Err() != nil {
		return unknownExpressionEstimate(fieldPath, expr)
	}
	est, err := checker.Cost(ast.NativeRep(), coster)
	if err != nil {
		return unknownExpressionEstimate(fieldPath, expr)
	}

	state, verdict := BudgetVerdict(est.Max, StateKnown, e.expressionLimit, false)
	return ExpressionEstimate{
		FieldPath:  fieldPath,
		Expression: expr,
		Cost:       est.Max,
		State:      state,
		Verdict:    verdict,
	}
}

// walkTemplate extracts and estimates all CEL expressions inside a runtime.RawExtension.
// If the raw bytes are not valid JSON, they are treated as a single string.
func (e *SpecEstimator) walkTemplate(raw *runtime.RawExtension, path string, env *cel.Env, coster *DeclTypeCostEstimator) []ExpressionEstimate {
	if raw == nil || len(raw.Raw) == 0 {
		return nil
	}
	var data any
	if err := json.Unmarshal(raw.Raw, &data); err != nil {
		data = string(raw.Raw)
	}
	w := templateWalker{est: e, env: env, coster: coster}
	w.walk(data, path)
	return w.out
}

// ResourceScope accumulates expressions belonging to a single ResourceTemplate or spec entry.
type ResourceScope struct {
	id          string
	est         *SpecEstimator
	coster      *DeclTypeCostEstimator
	whenEst     *ExpressionEstimate
	loopBound   uint64
	capExceeded bool
	loopState   SizeState
	loopExprEst *ExpressionEstimate
	bodyExprs   []ExpressionEstimate
}

// SetLoop configures forEach loop expansion for this resource.
func (r *ResourceScope) SetLoop(loop LoopInfo) {
	if loop.Bound > 0 {
		r.loopBound = loop.Bound
	}
	r.capExceeded = loop.CapExceeded
	if loop.State != "" {
		r.loopState = loop.State
	}
	r.loopExprEst = loop.Expr
	if loop.BodyCoster != nil {
		r.coster = loop.BodyCoster
	}
}

// AddWhen adds an unlooped precondition (includeWhen / when).
func (r *ResourceScope) AddWhen(env *cel.Env, fieldPath, rawExpr string) {
	if rawExpr == "" {
		return
	}
	est := r.est.estimateExpr(env, r.est.coster, fieldPath, rawExpr)
	r.whenEst = &est
}

// AddExpr adds a single expression evaluated in the resource body.
func (r *ResourceScope) AddExpr(env *cel.Env, fieldPath, rawExpr string) {
	if rawExpr == "" {
		return
	}
	est := r.est.estimateExpr(env, r.coster, fieldPath, rawExpr)
	r.bodyExprs = append(r.bodyExprs, est)
}

// AddTemplate walks a JSON template and adds all discovered expressions evaluated in the resource body.
func (r *ResourceScope) AddTemplate(env *cel.Env, fieldPath string, raw *runtime.RawExtension) {
	if raw == nil || len(raw.Raw) == 0 {
		return
	}
	exprs := r.est.walkTemplate(raw, fieldPath, env, r.coster)
	r.bodyExprs = append(r.bodyExprs, exprs...)
}

func (r *ResourceScope) build(budget uint64) ResourceEstimate {
	out := ResourceEstimate{
		ID:                 r.id,
		LoopBound:          r.loopBound,
		ForEachCapExceeded: r.capExceeded,
	}

	state := StateKnown
	var whenCost uint64
	if r.whenEst != nil {
		out.Expressions = append(out.Expressions, *r.whenEst)
		whenCost = r.whenEst.Cost
		if r.whenEst.State == StateUnknown {
			state = StateUnknown
		}
	}

	var forEachCost uint64
	if r.loopExprEst != nil {
		out.Expressions = append(out.Expressions, *r.loopExprEst)
		forEachCost = r.loopExprEst.Cost
	}
	if r.loopState == StateUnknown {
		state = StateUnknown
	}

	bodyTally := newCostTally()
	for _, be := range r.bodyExprs {
		out.Expressions = append(out.Expressions, be)
		bodyTally.add(be)
	}
	if bodyTally.state == StateUnknown {
		state = StateUnknown
	}

	anyOverBudget := false
	for _, expr := range out.Expressions {
		if expr.Verdict == VerdictOverBudget {
			anyOverBudget = true
			break
		}
	}

	out.TotalCost = SatAdd(SatAdd(whenCost, forEachCost), SatMul(out.LoopBound, bodyTally.cost))
	out.State, out.Verdict = BudgetVerdict(out.TotalCost, state, budget, anyOverBudget)
	return out
}

// templateWalker carries the fixed inputs and collected output of a single JSON template walk.
type templateWalker struct {
	est    *SpecEstimator
	env    *cel.Env
	coster *DeclTypeCostEstimator
	out    []ExpressionEstimate
}

func (w *templateWalker) walk(data any, path string) {
	switch v := data.(type) {
	case string:
		w.walkString(v, path)
	case map[string]any:
		w.walkMap(v, path)
	case []any:
		w.walkList(v, path)
	}
}

func (w *templateWalker) estimate(expr, path string) ExpressionEstimate {
	return w.est.estimateExpr(w.env, w.coster, path, expr)
}

// walkString estimates the CEL expressions embedded in a string value.
//
//   - "${expr}" (the whole string is one expression) yields that expression's estimate.
//   - "prefix ${a} middle ${b}" (interpolation) yields one combined estimate whose cost
//     is the sum of the inner expressions plus InterpolationBaseCharge per expression,
//     checked against the per-expression limit.
func (w *templateWalker) walkString(s, path string) {
	matches, err := template.FindCELExpressions(s)
	if err != nil || len(matches) == 0 {
		return
	}
	// if it's a standalone CEL expression
	if len(matches) == 1 && strings.TrimSpace(s) == matches[0].FullExpr {
		w.out = append(w.out, w.estimate(matches[0].InnerExpr, path))
		return
	}
	// if it's a string interpolation
	tally := newCostTally()
	tally.cost = uint64(len(matches)) * InterpolationBaseCharge
	for _, m := range matches {
		tally.add(w.estimate(m.InnerExpr, path))
	}

	state, verdict := BudgetVerdict(tally.cost, tally.state, w.est.expressionLimit, tally.anyOverBudget)
	w.out = append(w.out, ExpressionEstimate{
		FieldPath:  path,
		Expression: s,
		Cost:       tally.cost,
		State:      state,
		Verdict:    verdict,
	})
}

// walkMap estimates CEL expressions in map keys (e.g. "${name}": value) and recurses into values.
// Keys are visited in sorted order so the resulting estimates are deterministic.
func (w *templateWalker) walkMap(m map[string]any, path string) {
	for _, key := range slices.Sorted(maps.Keys(m)) {
		if strings.Contains(key, "${") {
			if matches, err := template.FindCELExpressions(key); err == nil {
				for _, match := range matches {
					w.out = append(w.out, w.estimate(match.InnerExpr, path+"[key]"))
				}
			}
		}
		w.walk(m[key], path+"."+key)
	}
}

func (w *templateWalker) walkList(list []any, path string) {
	for i, elem := range list {
		w.walk(elem, ItemPath(path, i, ""))
	}
}

// unknownExpressionEstimate is the result for an expression that could not be compiled or costed.
func unknownExpressionEstimate(fieldPath, expr string) ExpressionEstimate {
	return ExpressionEstimate{
		FieldPath:  fieldPath,
		Expression: expr,
		Cost:       uint64(MaxObjectSizeBytes),
		State:      StateUnknown,
		Verdict:    VerdictUnknown,
	}
}

// costTally tracks cumulative cost, state, and over-budget status for a list of expressions.
type costTally struct {
	cost          uint64
	state         SizeState
	anyOverBudget bool
}

func newCostTally() costTally {
	return costTally{state: StateKnown}
}

func (t *costTally) add(est ExpressionEstimate) {
	t.cost = SatAdd(t.cost, est.Cost)
	if est.State == StateUnknown {
		t.state = StateUnknown
	}
	if est.Verdict == VerdictOverBudget {
		t.anyOverBudget = true
	}
}

// ItemPath returns the field path of a list item: "<list>[<key>]" when key is set,
// otherwise "<list>[<index>]". For example ItemPath("spec.resources", 0, "db") is
// "spec.resources[db]" and ItemPath("spec.resources", 0, "") is "spec.resources[0]".
func ItemPath(list string, index int, key string) string {
	if key != "" {
		return fmt.Sprintf("%s[%s]", list, key)
	}
	return fmt.Sprintf("%s[%d]", list, index)
}

// UnwrapCEL strips an enclosing ${...} wrapper if present.
func UnwrapCEL(s string) string {
	trimmed := strings.TrimSpace(s)
	if strings.HasPrefix(trimmed, "${") && strings.HasSuffix(trimmed, "}") {
		return strings.TrimSpace(trimmed[2 : len(trimmed)-1])
	}
	return trimmed
}

// SatAdd performs saturating uint64 addition.
func SatAdd(a, b uint64) uint64 {
	if a > math.MaxUint64-b {
		return math.MaxUint64
	}
	return a + b
}

// SatMul performs saturating uint64 multiplication.
func SatMul(a, b uint64) uint64 {
	if a == 0 || b == 0 {
		return 0
	}
	if a > math.MaxUint64/b {
		return math.MaxUint64
	}
	return a * b
}
