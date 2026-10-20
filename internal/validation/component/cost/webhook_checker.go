// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package cost

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	apiextschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	openchoreodevv1alpha1 "github.com/openchoreo/openchoreo/api/v1alpha1"
	"github.com/openchoreo/openchoreo/internal/validation/component"
)

// Mode determines the webhook enforcement mode per Project 701 §4.8.
type Mode string

const (
	ModeOff     Mode = "off"
	ModeWarn    Mode = "warn"
	ModeEnforce Mode = "enforce"
)

// DefaultCostTimeout is the internal execution deadline for admission cost evaluation.
const DefaultCostTimeout = 2 * time.Second

// WebhookCostChecker executes admission cost analysis and ratchets for ComponentType and Trait objects.
type WebhookCostChecker struct {
	Mode    Mode
	Timeout time.Duration
}

// NewWebhookCostChecker creates a cost checker with default mode from CEL_COST_MODE (or warn) and 2s timeout.
func NewWebhookCostChecker(mode Mode) *WebhookCostChecker {
	if mode == "" {
		envMode := Mode(os.Getenv("CEL_COST_MODE"))
		switch envMode {
		case ModeEnforce, ModeWarn, ModeOff:
			mode = envMode
		default:
			mode = ModeWarn
		}
	}
	return &WebhookCostChecker{
		Mode:    mode,
		Timeout: DefaultCostTimeout,
	}
}

// ValidateComponentTypeCreate checks worst-case cost on ComponentType creation.
func (c *WebhookCostChecker) ValidateComponentTypeCreate(
	ctx context.Context,
	ct *openchoreodevv1alpha1.ComponentType,
	paramSchema, envConfigSchema *apiextschema.Structural,
) (admission.Warnings, error) {
	if c.Mode == ModeOff {
		return nil, nil
	}

	var warnings admission.Warnings
	resChan := make(chan struct {
		est TemplateEstimate
		err error
	}, 1)

	go func() {
		defer func() {
			if r := recover(); r != nil {
				resChan <- struct {
					est TemplateEstimate
					err error
				}{err: fmt.Errorf("panic during cost analysis: %v", r)}
			}
		}()

		val, err := component.NewCELValidator(component.ComponentTypeResource, component.SchemaOptions{
			ParametersSchema:         paramSchema,
			EnvironmentConfigsSchema: envConfigSchema,
		})
		if err != nil {
			resChan <- struct {
				est TemplateEstimate
				err error
			}{err: err}
			return
		}

		aggregator := NewTemplateAggregator(val)
		est, err := aggregator.EstimateComponentType(ct)
		resChan <- struct {
			est TemplateEstimate
			err error
		}{est: est, err: err}
	}()

	select {
	case <-time.After(c.Timeout):
		if c.Mode == ModeEnforce {
			return nil, fmt.Errorf("CEL cost analysis failed: evaluation deadline of %v exceeded", c.Timeout)
		}
		return admission.Warnings{"CEL cost analysis skipped: evaluation deadline exceeded"}, nil
	case res := <-resChan:
		if res.err != nil {
			if c.Mode == ModeEnforce {
				return nil, fmt.Errorf("CEL cost analysis failed: %w", res.err)
			}
			return admission.Warnings{fmt.Sprintf("CEL cost analysis skipped: %v", res.err)}, nil
		}

		est := res.est
		if est.Verdict == VerdictWithinBudget {
			return nil, nil
		}

		msg := fmt.Sprintf("ComponentType %q CEL cost verdict: %s (estimated cost %d / budget %d)",
			ct.GetName(), est.Verdict, est.TotalCost, MaxCumulativeRenderBudget)
		if diag := formatCostDiagnostics(est); diag != "" {
			msg = fmt.Sprintf("%s\n%s", msg, diag)
		}

		if c.Mode == ModeEnforce {
			return nil, fmt.Errorf("%s", msg)
		}
		// Warn mode (default)
		warnings = append(warnings, msg)
		return warnings, nil
	}
}

// ValidateTraitCreate checks worst-case cost on Trait creation.
func (c *WebhookCostChecker) ValidateTraitCreate(
	ctx context.Context,
	trait *openchoreodevv1alpha1.Trait,
	paramSchema, envConfigSchema *apiextschema.Structural,
) (admission.Warnings, error) {
	if c.Mode == ModeOff {
		return nil, nil
	}

	var warnings admission.Warnings
	resChan := make(chan struct {
		est TemplateEstimate
		err error
	}, 1)

	go func() {
		defer func() {
			if r := recover(); r != nil {
				resChan <- struct {
					est TemplateEstimate
					err error
				}{err: fmt.Errorf("panic during trait cost analysis: %v", r)}
			}
		}()

		val, err := component.NewCELValidator(component.TraitResource, component.SchemaOptions{
			ParametersSchema:         paramSchema,
			EnvironmentConfigsSchema: envConfigSchema,
		})
		if err != nil {
			resChan <- struct {
				est TemplateEstimate
				err error
			}{err: err}
			return
		}

		aggregator := NewTemplateAggregator(val)
		est, err := aggregator.EstimateTrait(trait)
		resChan <- struct {
			est TemplateEstimate
			err error
		}{est: est, err: err}
	}()

	select {
	case <-time.After(c.Timeout):
		if c.Mode == ModeEnforce {
			return nil, fmt.Errorf("CEL cost analysis failed: evaluation deadline of %v exceeded", c.Timeout)
		}
		return admission.Warnings{"CEL cost analysis skipped: evaluation deadline exceeded"}, nil
	case res := <-resChan:
		if res.err != nil {
			if c.Mode == ModeEnforce {
				return nil, fmt.Errorf("CEL cost analysis failed: %w", res.err)
			}
			return admission.Warnings{fmt.Sprintf("CEL cost analysis skipped: %v", res.err)}, nil
		}

		est := res.est
		if est.Verdict == VerdictWithinBudget {
			return nil, nil
		}

		msg := fmt.Sprintf("Trait %q CEL cost verdict: %s (estimated cost %d / budget %d)",
			trait.GetName(), est.Verdict, est.TotalCost, MaxCumulativeRenderBudget)
		if diag := formatCostDiagnostics(est); diag != "" {
			msg = fmt.Sprintf("%s\n%s", msg, diag)
		}

		if c.Mode == ModeEnforce {
			return nil, fmt.Errorf("%s", msg)
		}
		warnings = append(warnings, msg)
		return warnings, nil
	}
}

// formatCostDiagnostics extracts offending expressions and actionable guidance for cost violations.
func formatCostDiagnostics(est TemplateEstimate) string {
	var details []string
	for _, res := range est.Resources {
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
			} else if expr.Cost > MaxCumulativeRenderBudget || expr.Verdict == VerdictOverBudget {
				details = append(details, fmt.Sprintf("  * Resource %q: expression %q at %s exceeded budget (cost %d / budget %d; simplify nested comprehensions or reduce collection bounds)",
					res.ID, expr.Expression, expr.FieldPath, expr.Cost, MaxCumulativeRenderBudget))
			}
		}
	}
	if len(details) == 0 {
		return ""
	}
	return "Violations & Remediation Guidance:\n" + strings.Join(details, "\n")
}

// ValidateComponentTypeUpdate checks cost ratchets on ComponentType updates.
func (c *WebhookCostChecker) ValidateComponentTypeUpdate(
	ctx context.Context,
	oldCT, newCT *openchoreodevv1alpha1.ComponentType,
	paramSchema, envConfigSchema *apiextschema.Structural,
) (admission.Warnings, error) {
	if c.Mode == ModeOff {
		return nil, nil
	}

	var warnings admission.Warnings
	resChan := make(chan struct {
		verdict RatchetVerdict
		err     error
	}, 1)

	go func() {
		defer func() {
			if r := recover(); r != nil {
				resChan <- struct {
					verdict RatchetVerdict
					err     error
				}{err: fmt.Errorf("panic during cost ratchet evaluation: %v", r)}
			}
		}()

		val, err := component.NewCELValidator(component.ComponentTypeResource, component.SchemaOptions{
			ParametersSchema:         paramSchema,
			EnvironmentConfigsSchema: envConfigSchema,
		})
		if err != nil {
			resChan <- struct {
				verdict RatchetVerdict
				err     error
			}{err: err}
			return
		}

		aggregator := NewTemplateAggregator(val)
		ratchet := NewRatchetEvaluator(aggregator)
		v, err := ratchet.EvaluateRatchet(&oldCT.Spec, &newCT.Spec)
		resChan <- struct {
			verdict RatchetVerdict
			err     error
		}{verdict: v, err: err}
	}()

	select {
	case <-time.After(c.Timeout):
		if c.Mode == ModeEnforce {
			return nil, fmt.Errorf("CEL cost ratchet failed: evaluation deadline of %v exceeded", c.Timeout)
		}
		return admission.Warnings{"CEL cost ratchet skipped: evaluation deadline exceeded"}, nil
	case res := <-resChan:
		if res.err != nil {
			if c.Mode == ModeEnforce {
				return nil, fmt.Errorf("CEL cost ratchet failed: %w", res.err)
			}
			return admission.Warnings{fmt.Sprintf("CEL cost ratchet skipped: %v", res.err)}, nil
		}

		v := res.verdict
		for _, w := range v.Warnings {
			warnings = append(warnings, w)
		}

		if !v.Allowed {
			msg := fmt.Sprintf("ComponentType %q CEL cost ratchet violated: %v", newCT.GetName(), v.Violations)
			if c.Mode == ModeEnforce {
				return warnings, fmt.Errorf("%s", msg)
			}
			warnings = append(warnings, msg)
		}

		return warnings, nil
	}
}
