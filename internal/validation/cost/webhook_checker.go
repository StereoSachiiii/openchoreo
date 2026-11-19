// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package cost

import (
	"context"
	"fmt"
	"os"
	"reflect"
	goruntime "runtime"
	"sort"
	"strings"
	"time"

	"github.com/google/cel-go/cel"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrlLog "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"github.com/openchoreo/openchoreo/internal/limits"
	"github.com/openchoreo/openchoreo/internal/schema"
	"github.com/openchoreo/openchoreo/internal/template"
)

var costLog = ctrlLog.Log.WithName("cel-cost-webhook")

func objectName(obj runtime.Object) string {
	if meta, ok := obj.(metav1.Object); ok {
		return meta.GetName()
	}
	return ""
}

func objectNamespace(obj runtime.Object) string {
	if meta, ok := obj.(metav1.Object); ok {
		return meta.GetNamespace()
	}
	return ""
}

// isMetadataOnlyUpdate returns true when oldObj and newObj have the same concrete type,
// equal Spec and SchemaPair, and differing metadata (labels, annotations, finalizers, or
// deletionTimestamp) — e.g., GitOps tracking-annotation syncs or controller finalizer removal.
// Comparing Spec directly avoids relying on metadata.generation, which is not bumped on CRDs
// without a status subresource or in unit tests calling ValidateUpdate without an API server.
func isMetadataOnlyUpdate(oldObj, newObj runtime.Object, oldSchemas, newSchemas SchemaPair) bool {
	oldMeta, ok1 := oldObj.(metav1.Object)
	newMeta, ok2 := newObj.(metav1.Object)
	if !ok1 || !ok2 {
		return false
	}

	oldVal := reflect.ValueOf(oldObj)
	newVal := reflect.ValueOf(newObj)
	if oldVal.Type() != newVal.Type() {
		return false
	}
	if oldVal.Kind() == reflect.Ptr {
		if oldVal.IsNil() || newVal.IsNil() {
			return false
		}
		oldVal = oldVal.Elem()
		newVal = newVal.Elem()
	}
	if oldVal.Kind() != reflect.Struct {
		return false
	}

	oldSpec := oldVal.FieldByName("Spec")
	newSpec := newVal.FieldByName("Spec")
	if !oldSpec.IsValid() || !newSpec.IsValid() {
		return false
	}
	if !equality.Semantic.DeepEqual(oldSpec.Interface(), newSpec.Interface()) ||
		!equality.Semantic.DeepEqual(oldSchemas, newSchemas) {
		return false
	}

	return !equality.Semantic.DeepEqual(oldMeta.GetLabels(), newMeta.GetLabels()) ||
		!equality.Semantic.DeepEqual(oldMeta.GetAnnotations(), newMeta.GetAnnotations()) ||
		!equality.Semantic.DeepEqual(oldMeta.GetFinalizers(), newMeta.GetFinalizers()) ||
		!equality.Semantic.DeepEqual(oldMeta.GetDeletionTimestamp(), newMeta.GetDeletionTimestamp())
}

func (c *WebhookCostChecker) logAdmissionWarning(obj runtime.Object, outcome ValidationOutcome, warnings admission.Warnings) {
	if len(warnings) == 0 {
		return
	}
	costLog.Info("CEL cost admission warning",
		"kind", c.estimator.Kind(),
		"namespace", objectNamespace(obj),
		"name", objectName(obj),
		"mode", c.Mode,
		"outcome", outcome,
		"warnings", warnings,
	)
}

// forEachCapWarnings emits non-blocking admission warnings when a resource's forEach loop
// iterates over an author-defined schema field (parameters / environmentConfigs) whose
// bound is missing or exceeds the runtime per-loop cap (MaxForEachItemsPerLoop).
//
// Design rationale (why warn instead of hard-reject):
//   - Compute soundness is already guaranteed by clamping M = min(size, MaxForEachItemsPerLoop)
//     at admission and enforcing the identical 250-item per-loop / 2000-item per-render caps
//     in the runtime renderer (internal/template/foreach.go).
//   - Hard-rejecting ForEachCapExceeded would false-positive on filtered loops such as
//     forEach: ${parameters.items.filter(i, i.enabled)} (since loop-bound analysis strips
//     .filter() back to the source list's maxItems) and would break existing unbounded
//     sample templates without wiring ForEachCapExceeded into the update ratchet.
//   - Distinguishing missing bounds vs. explicit bounds > 250 gives platform engineers
//     actionable guidance without turning the cost estimator into a schema linter.
func forEachCapWarnings(est TemplateEstimate, schemas SchemaPair) admission.Warnings {
	var warnings admission.Warnings
	for _, r := range est.Resources {
		if !r.ForEachCapExceeded {
			continue
		}
		expr, ok := authorSchemaForEachExpr(r)
		if !ok {
			continue
		}
		if explicitBound, hasBound := lookupExplicitSchemaBound(schemas, expr); hasBound {
			warnings = append(warnings, fmt.Sprintf(
				"resource %q forEach iterates over %q whose schema bound (%d) exceeds the runtime per-loop cap (%d); rendering will fail if more than %d items are provided",
				r.ID, expr, explicitBound, MaxForEachItemsPerLoop, MaxForEachItemsPerLoop,
			))
		} else {
			warnings = append(warnings, fmt.Sprintf(
				"resource %q forEach iterates over %q which has no maxItems/maxProperties bound; add maxItems <= %d (runtime caps each forEach loop at %d items)",
				r.ID, expr, MaxForEachItemsPerLoop, MaxForEachItemsPerLoop,
			))
		}
	}
	return warnings
}

func authorSchemaForEachExpr(r ResourceEstimate) (string, bool) {
	for _, expr := range r.Expressions {
		if strings.HasSuffix(expr.FieldPath, ".forEach") {
			if strings.HasPrefix(expr.Expression, "parameters.") ||
				strings.HasPrefix(expr.Expression, "environmentConfigs.") {
				return expr.Expression, true
			}
		}
	}
	return "", false
}

func lookupExplicitSchemaBound(schemas SchemaPair, expr string) (int64, bool) {
	// Strip any trailing call segment like .filter(...) or .map(...)
	pathExpr := expr
	if idx := strings.IndexByte(pathExpr, '('); idx != -1 {
		if dot := strings.LastIndexByte(pathExpr[:idx], '.'); dot != -1 {
			pathExpr = pathExpr[:dot]
		}
	}
	parts := strings.Split(pathExpr, ".")
	if len(parts) < 2 {
		return 0, false
	}

	cur := schemas.Parameters
	if parts[0] == rootEnvironmentConfigs {
		cur = schemas.EnvironmentConfigs
	}
	if cur == nil {
		return 0, false
	}
	for _, seg := range parts[1:] {
		next, ok := cur.Properties[seg]
		if !ok {
			return 0, false
		}
		cp := next
		cur = &cp
	}
	if cur.ValueValidation == nil {
		return 0, false
	}
	if cur.ValueValidation.MaxItems != nil {
		return *cur.ValueValidation.MaxItems, true
	}
	if cur.ValueValidation.MaxProperties != nil {
		return *cur.ValueValidation.MaxProperties, true
	}
	return 0, false
}

// inspectDefaultBoundsWarnings collects warnings for omitted schema bounds in parameters and environmentConfigs.
func inspectDefaultBoundsWarnings(schemas SchemaPair) admission.Warnings {
	var warnings admission.Warnings
	bounds := limits.ActiveSchemaBounds()

	if schemas.Parameters != nil {
		for _, b := range schema.InspectDefaultBoundsOnStructural(schemas.Parameters, bounds) {
			warnings = append(warnings, b.WarningMessage("spec.parameters"))
		}
	}
	if schemas.EnvironmentConfigs != nil {
		for _, b := range schema.InspectDefaultBoundsOnStructural(schemas.EnvironmentConfigs, bounds) {
			warnings = append(warnings, b.WarningMessage("spec.environmentConfigs"))
		}
	}
	return warnings
}

var diagEnv, _ = cel.NewEnv(template.BaseCELExtensions()...)

// Mode determines the webhook enforcement mode (off, warn, or enforce).
type Mode string

const (
	ModeOff     Mode = "off"
	ModeWarn    Mode = "warn"
	ModeEnforce Mode = "enforce"
)

// DefaultCostTimeout is the internal execution deadline for admission cost evaluation.
const DefaultCostTimeout = 2 * time.Second

// WebhookCostChecker executes admission cost analysis and ratchets for a single template domain.
// It delegates domain-specific expression extraction to the injected DomainCostEstimator.
type WebhookCostChecker struct {
	Mode      Mode
	Timeout   time.Duration
	exprLimit uint64
	budget    uint64
	estimator DomainCostEstimator
	sem       chan struct{}
}

// acquireSlot reserves one concurrency slot before launching a cost-estimation goroutine,
// respecting ctx cancellation and the shared evaluation timer so bursts cannot queue unboundedly.
func (c *WebhookCostChecker) acquireSlot(ctx context.Context, timer *time.Timer) (warn string, err error, acquired bool) {
	if c.sem == nil {
		return "", nil, true
	}
	select {
	case c.sem <- struct{}{}:
		return "", nil, true
	case <-ctx.Done():
		if c.Mode == ModeEnforce {
			return "", fmt.Errorf("CEL cost analysis cancelled: %w", ctx.Err()), false
		}
		return fmt.Sprintf("CEL cost analysis skipped: %v", ctx.Err()), nil, false
	case <-timer.C:
		if c.Mode == ModeEnforce {
			return "", fmt.Errorf("CEL cost analysis failed: evaluation deadline of %v exceeded", c.Timeout), false
		}
		return "CEL cost analysis skipped: evaluation deadline exceeded", nil, false
	}
}

func (c *WebhookCostChecker) releaseSlot() {
	if c.sem != nil {
		<-c.sem
	}
}

// ValidateCreate evaluates worst-case static compute cost upon resource creation
// by dispatching to the injected DomainCostEstimator.
func (c *WebhookCostChecker) ValidateCreate(
	ctx context.Context,
	obj runtime.Object,
	schemas SchemaPair,
) (admission.Warnings, error) {
	defaultBoundsWarnings := inspectDefaultBoundsWarnings(schemas)
	if c.Mode == ModeOff {
		return defaultBoundsWarnings, nil
	}
	if c.estimator == nil {
		return defaultBoundsWarnings, fmt.Errorf("admission cost checker has no estimator configured")
	}

	start := time.Now()
	estOpts := []Option{WithExpressionLimit(c.exprLimit), WithBudget(c.budget)}

	timer := time.NewTimer(c.Timeout)
	defer timer.Stop()

	if warn, err, ok := c.acquireSlot(ctx, timer); !ok {
		RecordValidation(c.estimator.Kind(), c.Mode, OutcomeSaturated, time.Since(start))
		if err != nil {
			return defaultBoundsWarnings, err
		}
		return append(defaultBoundsWarnings, warn), nil
	}

	resChan := make(chan struct {
		estimate TemplateEstimate
		err      error
	}, 1)

	go func() {
		defer c.releaseSlot()
		defer func() {
			if r := recover(); r != nil {
				resChan <- struct {
					estimate TemplateEstimate
					err      error
				}{err: fmt.Errorf("panic during %s cost analysis: %v", c.estimator.Kind(), r)}
			}
		}()

		evalEstimate, err := c.estimator.Estimate(obj, schemas, estOpts...)
		resChan <- struct {
			estimate TemplateEstimate
			err      error
		}{estimate: evalEstimate, err: err}
	}()

	select {
	case <-ctx.Done():
		RecordValidation(c.estimator.Kind(), c.Mode, OutcomeTimeout, time.Since(start))
		if c.Mode == ModeEnforce {
			return defaultBoundsWarnings, fmt.Errorf("CEL cost analysis cancelled: %w", ctx.Err())
		}
		return append(defaultBoundsWarnings, fmt.Sprintf("CEL cost analysis skipped: %v", ctx.Err())), nil
	case <-timer.C:
		RecordValidation(c.estimator.Kind(), c.Mode, OutcomeTimeout, time.Since(start))
		if c.Mode == ModeEnforce {
			return defaultBoundsWarnings, fmt.Errorf("CEL cost analysis failed: evaluation deadline of %v exceeded", c.Timeout)
		}
		return append(defaultBoundsWarnings, "CEL cost analysis skipped: evaluation deadline exceeded"), nil
	case res := <-resChan:
		if res.err != nil {
			RecordValidation(c.estimator.Kind(), c.Mode, OutcomeError, time.Since(start))
			if c.Mode == ModeEnforce {
				return defaultBoundsWarnings, fmt.Errorf("CEL cost analysis failed: %w", res.err)
			}
			return append(defaultBoundsWarnings, fmt.Sprintf("CEL cost analysis skipped: %v", res.err)), nil
		}

		RecordEvaluation(c.estimator.Kind(), res.estimate.Verdict)
		outcome := outcomeForVerdict(res.estimate.Verdict)
		RecordValidation(c.estimator.Kind(), c.Mode, outcome, time.Since(start))
		defaultBoundsWarnings = append(defaultBoundsWarnings, forEachCapWarnings(res.estimate, schemas)...)
		if res.estimate.Verdict == VerdictWithinBudget {
			c.logAdmissionWarning(obj, outcome, defaultBoundsWarnings)
			return defaultBoundsWarnings, nil
		}

		msg := fmt.Sprintf("%s %q CEL cost verdict: %s (estimated cost %d / budget %d)",
			c.estimator.Kind(), objectName(obj), res.estimate.Verdict, res.estimate.TotalCost, c.budget)

		if diag := formatCostDiagnosticsWithSchema(c.exprLimit, c.budget, res.estimate, schemas); diag != "" {
			msg = fmt.Sprintf("%s\n%s", msg, diag)
		}

		if c.Mode == ModeEnforce {
			RecordRejection(c.estimator.Kind())
			return defaultBoundsWarnings, fmt.Errorf("%s", msg)
		}
		RecordWarning(c.estimator.Kind())
		defaultBoundsWarnings = append(defaultBoundsWarnings, msg)
		c.logAdmissionWarning(obj, outcome, defaultBoundsWarnings)
		return defaultBoundsWarnings, nil
	}
}

// ValidateUpdate evaluates worst-case static compute cost upon resource update
// by dispatching to the universal ratchet judge.
// It evaluates oldObj against oldSchemas and newObj against newSchemas so schema relaxations
// cannot bypass the non-increasing cost ratchet.
func (c *WebhookCostChecker) ValidateUpdate(
	ctx context.Context,
	oldObj, newObj runtime.Object,
	oldSchemas, newSchemas SchemaPair,
) (admission.Warnings, error) {
	defaultBoundsWarnings := inspectDefaultBoundsWarnings(newSchemas)
	if c.Mode == ModeOff {
		return defaultBoundsWarnings, nil
	}
	if c.estimator == nil {
		return defaultBoundsWarnings, fmt.Errorf("admission cost checker has no estimator configured")
	}
	if isMetadataOnlyUpdate(oldObj, newObj, oldSchemas, newSchemas) {
		return defaultBoundsWarnings, nil
	}

	start := time.Now()
	estOpts := []Option{WithExpressionLimit(c.exprLimit), WithBudget(c.budget)}

	timer := time.NewTimer(c.Timeout)
	defer timer.Stop()

	if warn, err, ok := c.acquireSlot(ctx, timer); !ok {
		RecordValidation(c.estimator.Kind(), c.Mode, OutcomeSaturated, time.Since(start))
		if err != nil {
			return defaultBoundsWarnings, err
		}
		return append(defaultBoundsWarnings, warn), nil
	}

	resChan := make(chan struct {
		oldEstimate TemplateEstimate
		newEstimate TemplateEstimate
		err         error
	}, 1)

	go func() {
		defer c.releaseSlot()
		defer func() {
			if r := recover(); r != nil {
				resChan <- struct {
					oldEstimate TemplateEstimate
					newEstimate TemplateEstimate
					err         error
				}{err: fmt.Errorf("panic during %s cost ratchet: %v", c.estimator.Kind(), r)}
			}
		}()

		oldEstimate, err := c.estimator.Estimate(oldObj, oldSchemas, estOpts...)
		if err != nil {
			resChan <- struct {
				oldEstimate TemplateEstimate
				newEstimate TemplateEstimate
				err         error
			}{err: err}
			return
		}

		newEstimate, err := c.estimator.Estimate(newObj, newSchemas, estOpts...)
		resChan <- struct {
			oldEstimate TemplateEstimate
			newEstimate TemplateEstimate
			err         error
		}{oldEstimate: oldEstimate, newEstimate: newEstimate, err: err}
	}()

	select {
	case <-ctx.Done():
		RecordValidation(c.estimator.Kind(), c.Mode, OutcomeTimeout, time.Since(start))
		if c.Mode == ModeEnforce {
			return defaultBoundsWarnings, fmt.Errorf("CEL cost analysis cancelled: %w", ctx.Err())
		}
		return append(defaultBoundsWarnings, fmt.Sprintf("CEL cost analysis skipped: %v", ctx.Err())), nil
	case <-timer.C:
		RecordValidation(c.estimator.Kind(), c.Mode, OutcomeTimeout, time.Since(start))
		if c.Mode == ModeEnforce {
			return defaultBoundsWarnings, fmt.Errorf("CEL cost analysis failed: evaluation deadline of %v exceeded", c.Timeout)
		}
		return append(defaultBoundsWarnings, "CEL cost analysis skipped: evaluation deadline exceeded"), nil
	case res := <-resChan:
		if res.err != nil {
			RecordValidation(c.estimator.Kind(), c.Mode, OutcomeError, time.Since(start))
			if c.Mode == ModeEnforce {
				return defaultBoundsWarnings, fmt.Errorf("CEL cost analysis failed: %w", res.err)
			}
			return append(defaultBoundsWarnings, fmt.Sprintf("CEL cost analysis skipped: %v", res.err)), nil
		}

		RecordEvaluation(c.estimator.Kind(), res.newEstimate.Verdict)
		defaultBoundsWarnings = append(defaultBoundsWarnings, forEachCapWarnings(res.newEstimate, newSchemas)...)
		allowed, warnMsg, violMsg := JudgeRatchet(c.estimator.Kind(), objectName(newObj), res.oldEstimate, res.newEstimate, c.budget)
		if allowed {
			if warnMsg != "" {
				RecordValidation(c.estimator.Kind(), c.Mode, OutcomeRatcheted, time.Since(start))
				RecordWarning(c.estimator.Kind())
				defaultBoundsWarnings = append(defaultBoundsWarnings, warnMsg)
				c.logAdmissionWarning(newObj, OutcomeRatcheted, defaultBoundsWarnings)
				return defaultBoundsWarnings, nil
			}
			RecordValidation(c.estimator.Kind(), c.Mode, OutcomeWithinBudget, time.Since(start))
			c.logAdmissionWarning(newObj, OutcomeWithinBudget, defaultBoundsWarnings)
			return defaultBoundsWarnings, nil
		}

		outcome := outcomeForVerdict(res.newEstimate.Verdict)
		RecordValidation(c.estimator.Kind(), c.Mode, outcome, time.Since(start))

		if diag := formatCostDiagnosticsWithSchema(c.exprLimit, c.budget, res.newEstimate, newSchemas); diag != "" {
			violMsg = fmt.Sprintf("%s\n%s", violMsg, diag)
		}

		if c.Mode == ModeEnforce {
			RecordRejection(c.estimator.Kind())
			return defaultBoundsWarnings, fmt.Errorf("%s", violMsg)
		}
		RecordWarning(c.estimator.Kind())
		defaultBoundsWarnings = append(defaultBoundsWarnings, violMsg)
		c.logAdmissionWarning(newObj, outcome, defaultBoundsWarnings)
		return defaultBoundsWarnings, nil
	}
}

// NewWebhookCostChecker creates a cost checker bound to a single DomainCostEstimator,
// with default mode from CEL_COST_MODE (or warn) and a 2s timeout.
// Without options it uses DefaultCELCostLimit and MaxCumulativeRenderBudget; see WithExpressionLimit and WithBudget.
// Panics if estimator is nil so misconfigured webhooks fail fast at startup.
func NewWebhookCostChecker(estimator DomainCostEstimator, mode Mode, opts ...Option) *WebhookCostChecker {
	if estimator == nil {
		panic("cost.NewWebhookCostChecker: estimator is required")
	}
	if mode == "" {
		envMode := Mode(os.Getenv("CEL_COST_MODE"))
		switch envMode {
		case ModeEnforce, ModeWarn, ModeOff:
			mode = envMode
		default:
			mode = ModeWarn
		}
	}
	cfg := resolveBudgetConfig(opts)
	maxConcurrency := max(2*goruntime.GOMAXPROCS(0), 2)
	return &WebhookCostChecker{
		Mode:      mode,
		Timeout:   DefaultCostTimeout,
		exprLimit: cfg.expressionLimit,
		budget:    cfg.budget,
		estimator: estimator,
		sem:       make(chan struct{}, maxConcurrency),
	}
}

func formatCostDiagnosticsWithSchema(
	exprLimit, budget uint64,
	estimate TemplateEstimate,
	schemas SchemaPair,
) string {
	if exprLimit == 0 {
		exprLimit = DefaultCELCostLimit
	}
	if budget == 0 {
		budget = limits.ReconcileBudget(exprLimit)
	}

	details, missingBounds := collectExpressionViolations(exprLimit, estimate, schemas)

	// If no individual expression failed but the cumulative total exceeds budget,
	// list the top expressions contributing to the cumulative spend.
	if len(details) == 0 && estimate.TotalCost > budget {
		details = formatCumulativeContributors(estimate, 5)
	}

	if len(details) == 0 {
		return ""
	}

	msg := "Violations & Remediation Guidance:\n" + strings.Join(details, "\n")
	if len(missingBounds) > 0 {
		msg += "\n\n" + formatMissingBounds(missingBounds)
	}
	return msg
}

// collectExpressionViolations inspects expressions across all resources for budget or state violations.
func collectExpressionViolations(
	exprLimit uint64,
	estimate TemplateEstimate,
	schemas SchemaPair,
) (details []string, missingBounds []MissingSchemaBound) {
	seenMissing := make(map[string]bool)
	if exprLimit == 0 {
		exprLimit = DefaultCELCostLimit
	}
	paramSchema := schemas.Parameters
	envConfigSchema := schemas.EnvironmentConfigs

	for _, res := range estimate.Resources {
		if res.Verdict == VerdictWithinBudget && res.State == StateKnown {
			continue
		}
		for _, expr := range res.Expressions {
			if expr.State == StateUnknown {
				hint := "ensure input collections have explicit maxItems or maxLength defined in openAPIV3Schema"
				if strings.Contains(expr.Expression, "parameters.") {
					hint = "add 'maxItems' to array parameters in spec.parameters.openAPIV3Schema so CEL static cost can be bounded"
				}
				details = append(details, fmt.Sprintf("  * Resource %q: expression %q at %s evaluated as UNKNOWN (%s)",
					res.ID, expr.Expression, expr.FieldPath, hint))
			} else if expr.Cost > exprLimit || expr.Verdict == VerdictOverBudget {
				reason := "cost exceeded single expression limit; simplify nested comprehensions or reduce collection bounds"
				fix := "reduce maxItems in openAPIV3Schema or split logic across separate resources"
				if strings.Contains(expr.Expression, ".map(") && (strings.Contains(expr.Expression, ".filter(") || strings.Count(expr.Expression, ".map(") > 1) {
					reason = "nested loops over collections"
					fix = "set maxLength on elements, lower maxItems in openAPIV3Schema, or avoid nesting loops over the same field"
				}
				details = append(details, fmt.Sprintf("  * Resource %q (%s): expression %q cost %d / limit %d: %s. Fix: %s",
					res.ID, expr.FieldPath, expr.Expression, expr.Cost, exprLimit, reason, fix))
			}

			// Parse AST and detect missing OpenAPI schema constraints
			if (paramSchema != nil || envConfigSchema != nil) && diagEnv != nil {
				exprsToInspect := []string{expr.Expression}
				if matches, err := template.FindCELExpressions(expr.Expression); err == nil && len(matches) > 0 {
					exprsToInspect = make([]string, 0, len(matches))
					for _, m := range matches {
						exprsToInspect = append(exprsToInspect, m.InnerExpr)
					}
				}
				for _, rawCEL := range exprsToInspect {
					clean := UnwrapCEL(rawCEL)
					if parsed, issues := diagEnv.Parse(clean); issues == nil || issues.Err() == nil {
						for _, mb := range InspectMissingBounds(parsed.NativeRep().Expr(), paramSchema, envConfigSchema) {
							key := mb.FieldPath + ":" + mb.MissingLimit
							if !seenMissing[key] {
								seenMissing[key] = true
								missingBounds = append(missingBounds, mb)
							}
						}
					}
				}
			}
		}
	}

	return details, missingBounds
}

type exprCostContribution struct {
	resID     string
	expr      string
	fieldPath string
	cost      uint64
}

// formatCumulativeContributors formats top-N expressions that contribute to a cumulative template budget breach.
func formatCumulativeContributors(estimate TemplateEstimate, topN int) []string {
	var contribs []exprCostContribution
	for _, res := range estimate.Resources {
		for _, expr := range res.Expressions {
			if expr.Cost > 0 {
				contribs = append(contribs, exprCostContribution{
					resID:     res.ID,
					expr:      expr.Expression,
					fieldPath: expr.FieldPath,
					cost:      expr.Cost,
				})
			}
		}
	}

	sort.Slice(contribs, func(i, j int) bool {
		return contribs[i].cost > contribs[j].cost
	})

	var lines []string
	lines = append(lines, "  No single expression exceeded the individual budget ceiling, but cumulative template render cost exceeded the budget.")
	lines = append(lines, "  Top contributing expressions to the cumulative cost:")

	limit := min(topN, len(contribs))
	for i := 0; i < limit; i++ {
		c := contribs[i]
		lines = append(lines, fmt.Sprintf("  * Resource %q: expression %q at %s (cost: %d)",
			c.resID, c.expr, c.fieldPath, c.cost))
	}
	lines = append(lines, "  Remediation: reduce declared maxItems on collections or simplify comprehensions across these resources.")
	return lines
}

// formatMissingBounds formats actionable remediation blocks for missing OpenAPI schema bounds.
func formatMissingBounds(missingBounds []MissingSchemaBound) string {
	lines := make([]string, 0, 1+len(missingBounds))
	lines = append(lines, "Missing OpenAPI Schema Constraints Detected:")
	for _, mb := range missingBounds {
		var assumedDesc string
		switch mb.MissingLimit {
		case "maxItems":
			assumedDesc = fmt.Sprintf("%d items", mb.AssumedValue)
		case "maxLength":
			assumedDesc = fmt.Sprintf("%d characters", mb.AssumedValue)
		case "maxProperties":
			assumedDesc = fmt.Sprintf("%d properties", mb.AssumedValue)
		}
		lines = append(lines, fmt.Sprintf("• Field %q: missing %s (assumed %s)\n  %s",
			mb.FieldPath, mb.MissingLimit, assumedDesc, mb.Remediation))
	}
	return strings.Join(lines, "\n")
}
