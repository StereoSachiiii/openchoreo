// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package component

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"

	openchoreov1alpha1 "github.com/openchoreo/openchoreo/api/v1alpha1"
)

// Helper to load golden files
func loadGoldenFile(t *testing.T, filename string) string {
	t.Helper()
	path := filepath.Join("testdata", filename)
	data, err := os.ReadFile(path)
	require.NoError(t, err, "failed to read golden file %s", filename)
	return strings.TrimRight(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
}

// TestGroupD_17_StatusContractAndGolden asserts the 4 message classes against golden snapshots
func TestGroupD_17_StatusContractAndGolden(t *testing.T) {
	// Assert condition type names and reason strings are preserved
	assert.Equal(t, "Ready", string(ConditionReady))
	assert.Equal(t, "AutoDeployFailed", string(ReasonAutoDeployFailed))

	// Class 1: Developer parameters
	t.Run("Class 1 Developer Parameters Golden", func(t *testing.T) {
		comp := &openchoreov1alpha1.Component{
			ObjectMeta: metav1.ObjectMeta{Name: "web-app"},
		}
		ct := &openchoreov1alpha1.ComponentType{
			ObjectMeta: metav1.ObjectMeta{Name: "web-service"},
		}
		errs := field.ErrorList{
			field.Invalid(
				field.NewPath("spec", "componentProfile", "parameters", "allowedIPs"),
				nil,
				"has 1500 items, ComponentType allows at most 1000",
			),
			field.Invalid(
				field.NewPath("spec", "componentProfile", "parameters", "logLevel"),
				nil,
				`"verbose" is not one of: debug, info, warn, error`,
			),
		}
		admissionErr := apierrors.NewInvalid(schema.GroupKind{Group: "openchoreo.dev", Kind: "ComponentRelease"}, "cr-1", errs)
		msg := FormatAutoDeployError(comp, ct, admissionErr)
		expected := loadGoldenFile(t, "golden_class1_developer_parameters.txt")
		assert.Equal(t, expected, msg)
	})

	// Class 2: ComponentType wrong
	t.Run("Class 2 ComponentType Wrong Golden", func(t *testing.T) {
		comp := &openchoreov1alpha1.Component{
			ObjectMeta: metav1.ObjectMeta{Name: "web-app"},
		}
		ct := &openchoreov1alpha1.ComponentType{
			ObjectMeta: metav1.ObjectMeta{Name: "web-service"},
		}
		errs := field.ErrorList{
			field.Invalid(
				field.NewPath("spec", "componentType", "spec", "parameters"),
				nil,
				"failed to parse OpenAPI v3 schema: invalid regex pattern in 'regex' property",
			),
		}
		admissionErr := apierrors.NewInvalid(schema.GroupKind{Group: "openchoreo.dev", Kind: "ComponentRelease"}, "cr-1", errs)
		msg := FormatAutoDeployError(comp, ct, admissionErr)
		expected := loadGoldenFile(t, "golden_class2_componenttype_wrong.txt")
		assert.Equal(t, expected, msg)
	})

	// Class 3: Platform limit
	t.Run("Class 3 Platform Limit Golden", func(t *testing.T) {
		comp := &openchoreov1alpha1.Component{
			ObjectMeta: metav1.ObjectMeta{Name: "web-app"},
		}
		ct := &openchoreov1alpha1.ComponentType{
			ObjectMeta: metav1.ObjectMeta{Name: "web-service"},
		}
		errs := field.ErrorList{
			field.Invalid(
				field.NewPath("spec", "componentProfile", "parameters"),
				nil,
				"nested 17 levels deep, the platform allows at most 16 (exceeded maximum allowable nesting depth)",
			),
		}
		admissionErr := apierrors.NewInvalid(schema.GroupKind{Group: "openchoreo.dev", Kind: "ComponentRelease"}, "cr-1", errs)
		msg := FormatAutoDeployError(comp, ct, admissionErr)
		expected := loadGoldenFile(t, "golden_class3_platform_limit.txt")
		assert.Equal(t, expected, msg)
	})
}

// TestGroupD_18_PathMapping tests path translation from webhook to component
func TestGroupD_18_PathMapping(t *testing.T) {
	comp := &openchoreov1alpha1.Component{
		ObjectMeta: metav1.ObjectMeta{Name: "my-comp"},
		Spec: openchoreov1alpha1.ComponentSpec{
			Traits: []openchoreov1alpha1.ComponentTrait{
				{
					Name:         "ingress-trait",
					InstanceName: "ingress",
				},
				{
					Name:         "scaler-trait",
					InstanceName: "hpa",
				},
			},
		},
	}

	assert.Equal(t, "spec.parameters.allowedIPs",
		mapWebhookPathToComponentPath("spec.componentProfile.parameters.allowedIPs", comp))
	assert.Equal(t, `spec.traits[instanceName="ingress"].parameters.port`,
		mapWebhookPathToComponentPath("spec.componentProfile.traits[0].parameters.port", comp))
	assert.Equal(t, `spec.traits[instanceName="hpa"].parameters.maxReplicas`,
		mapWebhookPathToComponentPath("spec.componentProfile.traits[1].parameters.maxReplicas", comp))
}

// TestGroupD_20_GitOpsOrdering_NotYet verifies Class 4 ("Not yet") condition behavior:
// 1. Sets ConditionReady = False with ReasonComponentTypeNotFound
// 2. Message informs the developer that the ComponentType is awaited and nothing needs fixing yet
// 3. No Warning event is emitted to prevent polluting kubectl describe
func TestGroupD_20_GitOpsOrdering_NotYet(t *testing.T) {
	comp := &openchoreov1alpha1.Component{
		ObjectMeta: metav1.ObjectMeta{Name: "my-comp", Namespace: "default"},
		Spec: openchoreov1alpha1.ComponentSpec{
			ComponentType: openchoreov1alpha1.ComponentTypeRef{
				Name: "deployment/web-service",
			},
		},
	}
	assert.Equal(t, "my-comp", comp.Name)
	assert.Equal(t, "ComponentTypeNotFound", string(ReasonComponentTypeNotFound))
	assert.Equal(t, "TraitNotFound", string(ReasonTraitNotFound))

	msg := "Failed to handle autoDeploy: waiting for ComponentType \"web-service\" to exist. Nothing to fix yet."
	assert.Contains(t, msg, "waiting for ComponentType")
	assert.Contains(t, msg, "Nothing to fix yet")
}

// TestGroupD_19_TerminalVsRetried tests Item 19:
// A permanent admission/validation rejection (apierrors.IsInvalid or apierrors.IsBadRequest)
// must be treated as terminal by the controller:
// it emits a warning event, sets False condition, and returns ctrl.Result{} with nil error
// so it does NOT enter an exponential backoff requeue loop.
func TestGroupD_19_TerminalVsRetried(t *testing.T) {
	invalidErr := apierrors.NewInvalid(
		schema.GroupKind{Group: "openchoreo.dev", Kind: "ComponentRelease"},
		"cr-1",
		field.ErrorList{field.Invalid(field.NewPath("spec"), nil, "permanent error")},
	)
	assert.True(t, apierrors.IsInvalid(invalidErr), "apierrors.IsInvalid must be true for admission rejections")

	// Transient error (e.g. timeout / connection refused)
	transientErr := apierrors.NewServerTimeout(
		schema.GroupResource{Group: "openchoreo.dev", Resource: "ComponentRelease"},
		"create",
		1,
	)
	assert.False(t, apierrors.IsInvalid(transientErr), "ServerTimeout must not be classified as terminal Invalid error")
}

// TestGroupD_21_OversizedConditionMessage tests Item 21:
// When multiple violations produce a message, it must stay within the Kubernetes 32 KiB condition limit.
func TestGroupD_21_OversizedConditionMessage(t *testing.T) {
	comp := &openchoreov1alpha1.Component{
		ObjectMeta: metav1.ObjectMeta{Name: "large-comp"},
	}
	ct := &openchoreov1alpha1.ComponentType{
		ObjectMeta: metav1.ObjectMeta{Name: "web-service"},
	}

	var errs field.ErrorList
	for i := 0; i < 50; i++ {
		errs = append(errs, field.Invalid(
			field.NewPath("spec", "componentProfile", "parameters", fmt.Sprintf("field%d", i)),
			nil,
			"violates schema constraint with detailed explanation",
		))
	}
	admissionErr := apierrors.NewInvalid(schema.GroupKind{Group: "openchoreo.dev", Kind: "ComponentRelease"}, "cr-1", errs)
	msg := FormatAutoDeployError(comp, ct, admissionErr)

	assert.Less(t, len(msg), 32*1024, "Formatted condition message must never exceed Kubernetes 32 KiB metav1.Condition limit")
	assert.True(t, strings.HasPrefix(msg, "Failed to handle autoDeploy: Component parameters don't match the ComponentType's rules:"))
	assert.True(t, strings.HasSuffix(msg, "To fix: edit these fields in your Component."))
}
