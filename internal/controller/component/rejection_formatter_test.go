// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package component

import (
	"testing"

	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"

	openchoreov1alpha1 "github.com/openchoreo/openchoreo/api/v1alpha1"
)

func TestFormatAutoDeployError_Class1_DeveloperParameters(t *testing.T) {
	comp := &openchoreov1alpha1.Component{
		ObjectMeta: metav1.ObjectMeta{Name: "my-comp"},
		Spec: openchoreov1alpha1.ComponentSpec{
			Traits: []openchoreov1alpha1.ComponentTrait{
				{
					Name:         "ingress-trait",
					InstanceName: "ingress",
				},
			},
		},
	}
	ct := &openchoreov1alpha1.ComponentType{
		ObjectMeta: metav1.ObjectMeta{Name: "web-service"},
	}

	errs := field.ErrorList{
		field.Invalid(
			field.NewPath("spec", "componentProfile", "parameters", "allowedIPs"),
			"1500 items",
			"has 1500 items, ComponentType allows at most 1000",
		),
		field.Invalid(
			field.NewPath("spec", "componentProfile", "parameters", "logLevel"),
			"verbose",
			`"verbose" is not one of: debug, info, warn, error`,
		),
		field.Invalid(
			field.NewPath("spec", "componentProfile", "traits").Index(0).Child("parameters", "timeoutSeconds"),
			120,
			"120 exceeds maximum 60",
		),
	}
	admissionErr := apierrors.NewInvalid(schema.GroupKind{Group: "openchoreo.dev", Kind: "ComponentRelease"}, "cr-1", errs)

	msg := FormatAutoDeployError(comp, ct, admissionErr)

	expected := `Failed to handle autoDeploy: Component parameters don't match the ComponentType's rules:
  • spec.parameters.allowedIPs: has 1500 items, ComponentType allows at most 1000
  • spec.parameters.logLevel: "verbose" is not one of: debug, info, warn, error
  • spec.traits[instanceName="ingress"].parameters.timeoutSeconds: 120 exceeds maximum 60
To fix: edit these fields in your Component.`

	require.Equal(t, expected, msg)
}

func TestFormatAutoDeployError_Class2_ComponentTypeWrong(t *testing.T) {
	comp := &openchoreov1alpha1.Component{
		ObjectMeta: metav1.ObjectMeta{Name: "my-comp"},
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

	expected := `Failed to handle autoDeploy: the ComponentType "web-service" can't be used as written:
  • spec.componentType.spec.parameters: failed to parse OpenAPI v3 schema: invalid regex pattern in 'regex' property
To fix: your parameters are not the cause. Ask your platform team to correct the ComponentType.`

	require.Equal(t, expected, msg)
}

func TestFormatAutoDeployError_Class3_PlatformLimit(t *testing.T) {
	comp := &openchoreov1alpha1.Component{
		ObjectMeta: metav1.ObjectMeta{Name: "my-comp"},
	}
	ct := &openchoreov1alpha1.ComponentType{
		ObjectMeta: metav1.ObjectMeta{Name: "web-service"},
	}

	errs := field.ErrorList{
		field.Invalid(
			field.NewPath("spec", "componentProfile", "parameters"),
			17,
			"nested 17 levels deep, the platform allows at most 16 (exceeded maximum allowable nesting depth)",
		),
	}
	admissionErr := apierrors.NewInvalid(schema.GroupKind{Group: "openchoreo.dev", Kind: "ComponentRelease"}, "cr-1", errs)

	msg := FormatAutoDeployError(comp, ct, admissionErr)

	expected := `Failed to handle autoDeploy: parameters exceed a platform-wide limit:
  • spec.parameters: nested 17 levels deep, the platform allows at most 16 (exceeded maximum allowable nesting depth)
    (this limit is not set by your ComponentType)
To fix: flatten the structure, or ask your platform team if the limit can change.`

	require.Equal(t, expected, msg)
}
