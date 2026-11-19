// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package component

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	openchoreov1alpha1 "github.com/openchoreo/openchoreo/api/v1alpha1"
)

// traitIndexRegex matches webhook trait field path index: spec.componentProfile.traits[<index>].
var traitIndexRegex = regexp.MustCompile(`^spec\.componentProfile\.traits\[(\d+)\](\..+)?$`)

// invalidPrefixRegex strips standard Kubernetes `Invalid value: ...: ` or `Required value: ` prefix from cause.Message.
var invalidPrefixRegex = regexp.MustCompile(`^(Invalid value: [^:]+: |Required value: |Duplicate value: [^:]+: )`)

// cleanCauseMessage cleans up boilerplate Kubernetes API cause strings.
func cleanCauseMessage(msg string) string {
	cleaned := invalidPrefixRegex.ReplaceAllString(msg, "")
	return strings.TrimSpace(cleaned)
}

// FormatAutoDeployError formats an admission rejection or autoDeploy failure into an actionable,
// developer-centric condition message following the required 4-class convention:
//  1. Developer parameters break ComponentType rules:
//     Failed to handle autoDeploy: Component parameters don't match the ComponentType's rules:
//     • spec.parameters.<field>: <actual> violates schema ...
//     To fix: edit these fields in your Component.
//  2. ComponentType itself is wrong:
//     Failed to handle autoDeploy: the ComponentType "<name>" can't be used as written:
//     • <issue>
//     To fix: your parameters are not the cause. Ask your platform team to correct the ComponentType.
//  3. Platform limit exceeded:
//     Failed to handle autoDeploy: parameters exceed a platform-wide limit:
//     • <issue>
//     To fix: <remediation>
//  4. Pending dependencies / GitOps ordering:
//     Failed to handle autoDeploy: waiting for ComponentType "<name>" to exist. Nothing to fix yet.
func FormatAutoDeployError(comp *openchoreov1alpha1.Component, ct *openchoreov1alpha1.ComponentType, err error) string {
	if err == nil {
		return ""
	}

	var statusErr *apierrors.StatusError
	if !errors.As(err, &statusErr) || statusErr.ErrStatus.Reason != metav1.StatusReasonInvalid || statusErr.ErrStatus.Details == nil {
		return fmt.Sprintf("Failed to handle autoDeploy: %v", err)
	}

	ctName := ""
	if ct != nil {
		ctName = ct.Name
	}
	if ctName == "" && comp != nil && comp.Spec.ComponentType.Name != "" {
		ctName = comp.Spec.ComponentType.Name
	}

	causes := statusErr.ErrStatus.Details.Causes
	if len(causes) == 0 {
		return fmt.Sprintf("Failed to handle autoDeploy: %v", err)
	}

	devItems, ctItems, platformItems := classifyAutoDeployCauses(causes, comp)

	// Class 3: Platform-wide limit
	if len(platformItems) > 0 && len(devItems) == 0 && len(ctItems) == 0 {
		return formatPlatformLimitRejection(platformItems)
	}

	// Class 2: ComponentType itself can't be used as written
	if len(ctItems) > 0 && len(devItems) == 0 {
		return formatComponentTypeRejection(ctName, ctItems)
	}

	// Class 1: Developer parameters don't match ComponentType rules
	lines := make([]string, 0, 2+len(devItems)+len(platformItems))
	lines = append(lines, "Failed to handle autoDeploy: Component parameters don't match the ComponentType's rules:")
	for _, item := range devItems {
		lines = append(lines, fmt.Sprintf("  • %s: %s", item.fieldPath, item.message))
	}
	for _, item := range platformItems {
		lines = append(lines, fmt.Sprintf("  • %s: %s", item.fieldPath, item.message))
	}
	lines = append(lines, "To fix: edit these fields in your Component.")
	return strings.Join(lines, "\n")
}

type violationItem struct {
	fieldPath string
	message   string
}

func classifyAutoDeployCauses(causes []metav1.StatusCause, comp *openchoreov1alpha1.Component) (devItems, ctItems, platformItems []violationItem) {
	for _, cause := range causes {
		fld := cause.Field
		msg := cleanCauseMessage(cause.Message)
		item := violationItem{
			fieldPath: mapWebhookPathToComponentPath(fld, comp),
			message:   msg,
		}

		switch {
		case isPlatformLimitCause(msg):
			platformItems = append(platformItems, item)
		case isComponentTypeCause(fld, msg):
			ctItems = append(ctItems, item)
		default:
			devItems = append(devItems, item)
		}
	}
	return devItems, ctItems, platformItems
}

func isPlatformLimitCause(msg string) bool {
	return strings.Contains(msg, "maximum allowable nesting depth") ||
		strings.Contains(msg, "platform-wide limit") ||
		strings.Contains(msg, "exceeds maximum allowed payload size")
}

func isComponentTypeCause(fld, msg string) bool {
	return strings.HasPrefix(fld, "spec.componentType") ||
		strings.HasPrefix(fld, "spec.traits") ||
		strings.Contains(msg, "failed to parse") ||
		strings.Contains(msg, "invalid schema definition")
}

func formatPlatformLimitRejection(platformItems []violationItem) string {
	lines := make([]string, 0, 2+len(platformItems))
	lines = append(lines, "Failed to handle autoDeploy: parameters exceed a platform-wide limit:")
	isNesting := false
	for _, item := range platformItems {
		if strings.Contains(item.message, "nesting depth") {
			isNesting = true
		}
		lines = append(lines, fmt.Sprintf("  • %s: %s\n    (this limit is not set by your ComponentType)", item.fieldPath, item.message))
	}
	if isNesting {
		lines = append(lines, "To fix: flatten the structure, or ask your platform team if the limit can change.")
	} else {
		lines = append(lines, "To fix: reduce parameter payload size, or ask your platform team if the limit can change.")
	}
	return strings.Join(lines, "\n")
}

func formatComponentTypeRejection(ctName string, ctItems []violationItem) string {
	displayName := ctName
	if displayName == "" {
		displayName = "specified"
	}
	lines := make([]string, 0, 2+len(ctItems))
	lines = append(lines, fmt.Sprintf("Failed to handle autoDeploy: the ComponentType %q can't be used as written:", displayName))
	for _, item := range ctItems {
		if item.fieldPath != "" {
			lines = append(lines, fmt.Sprintf("  • %s: %s", item.fieldPath, item.message))
		} else {
			lines = append(lines, fmt.Sprintf("  • %s", item.message))
		}
	}
	lines = append(lines, "To fix: your parameters are not the cause. Ask your platform team to correct the ComponentType.")
	return strings.Join(lines, "\n")
}

// mapWebhookPathToComponentPath translates ComponentRelease webhook field paths to the
// corresponding developer-facing Component field paths.
func mapWebhookPathToComponentPath(webhookPath string, comp *openchoreov1alpha1.Component) string {
	if webhookPath == "" {
		return "spec.parameters"
	}

	// Replace spec.componentProfile.parameters -> spec.parameters
	if strings.HasPrefix(webhookPath, "spec.componentProfile.parameters") {
		return strings.Replace(webhookPath, "spec.componentProfile.parameters", "spec.parameters", 1)
	}

	// Match trait paths: spec.componentProfile.traits[<index>]
	if m := traitIndexRegex.FindStringSubmatch(webhookPath); len(m) > 1 {
		idx, err := strconv.Atoi(m[1])
		suffix := ""
		if len(m) > 2 {
			suffix = m[2]
		}
		if err == nil && comp != nil && idx >= 0 && idx < len(comp.Spec.Traits) {
			instanceName := comp.Spec.Traits[idx].InstanceName
			return fmt.Sprintf("spec.traits[instanceName=%q]%s", instanceName, suffix)
		}
		// If index is outside bounds or cannot match, fallback to standard indexing
		return fmt.Sprintf("spec.traits[%d]%s", idx, suffix)
	}

	// Remove spec.componentProfile prefix if present
	if strings.HasPrefix(webhookPath, "spec.componentProfile.") {
		return strings.Replace(webhookPath, "spec.componentProfile.", "spec.", 1)
	}

	return webhookPath
}
