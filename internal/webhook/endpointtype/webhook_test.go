// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package endpointtype

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"

	openchoreodevv1alpha1 "github.com/openchoreo/openchoreo/api/v1alpha1"
	"github.com/openchoreo/openchoreo/internal/validation/cost"
)

// The endpoint-type validator's contract is covered in
// internal/validation/endpoint/endpointtype_test.go; these only verify
// dispatch through ValidateCreate/Update/Delete and the error shape.
func route(name string) openchoreodevv1alpha1.ResourceTemplate {
	return openchoreodevv1alpha1.ResourceTemplate{
		ID: "route",
		Template: &runtime.RawExtension{Raw: []byte(
			`{"apiVersion":"gateway.networking.k8s.io/v1","kind":"HTTPRoute","metadata":{"name":"` + name + `"}}`)},
	}
}

var _ = Describe("EndpointType Webhook", func() {
	var (
		ctx       context.Context
		validator Validator
	)

	BeforeEach(func() {
		ctx = context.Background()
		validator = Validator{}
	})

	newType := func(tmpl openchoreodevv1alpha1.ResourceTemplate) *openchoreodevv1alpha1.EndpointType {
		return &openchoreodevv1alpha1.EndpointType{
			Spec: openchoreodevv1alpha1.EndpointTypeSpec{Resources: []openchoreodevv1alpha1.ResourceTemplate{tmpl}},
		}
	}

	It("admits a valid spec on create and update", func() {
		obj := newType(route("${metadata.name}"))
		_, err := validator.ValidateCreate(ctx, obj)
		Expect(err).ToNot(HaveOccurred())
		_, err = validator.ValidateUpdate(ctx, obj.DeepCopy(), obj)
		Expect(err).ToNot(HaveOccurred())
	})

	It("rejects a template referencing an unknown variable as Invalid", func() {
		obj := newType(route("${workload.name}"))
		_, err := validator.ValidateCreate(ctx, obj)
		Expect(err).To(HaveOccurred())
		Expect(apierrors.IsInvalid(err)).To(BeTrue())
		Expect(err.Error()).To(ContainSubstring("spec.resources[0].template"))
	})

	It("re-validates on update", func() {
		_, err := validator.ValidateUpdate(ctx, newType(route("${metadata.name}")), newType(route("${workload.name}")))
		Expect(err).To(HaveOccurred())
	})

	It("admits deletes", func() {
		_, err := validator.ValidateDelete(ctx, newType(route("${workload.name}")))
		Expect(err).ToNot(HaveOccurred())
	})

	It("rejects the wrong object type", func() {
		_, err := validator.ValidateCreate(ctx, &openchoreodevv1alpha1.ClusterEndpointType{})
		Expect(err).To(HaveOccurred())
	})

	Context("CEL Cost Enforcement", func() {
		overbudgetType := func() *openchoreodevv1alpha1.EndpointType {
			return &openchoreodevv1alpha1.EndpointType{
				Spec: openchoreodevv1alpha1.EndpointTypeSpec{
					Parameters: &openchoreodevv1alpha1.SchemaSection{
						OpenAPIV3Schema: &runtime.RawExtension{
							Raw: []byte(`{"type":"object","properties":{"hosts":{"type":"array","maxItems":60,"items":{"type":"string"}}}}`),
						},
					},
					Resources: []openchoreodevv1alpha1.ResourceTemplate{
						{
							ID: "route",
							Template: &runtime.RawExtension{
								Raw: []byte(`{"apiVersion":"gateway.networking.k8s.io/v1","kind":"HTTPRoute","metadata":{"name":"${metadata.name}"},"spec":{"hostnames":["${parameters.hosts.map(h1, parameters.hosts.map(h2, parameters.hosts.map(h3, h1 + h2 + h3))).size()}"]}}`),
							},
						},
					},
				},
			}
		}

		It("enforce mode rejects an over-budget EndpointType on create", func() {
			enforceVal := NewValidator(cost.ModeEnforce)
			warnings, err := enforceVal.ValidateCreate(ctx, overbudgetType())
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("CEL cost verdict: OVER BUDGET"))
			Expect(warnings).To(BeEmpty())
		})

		It("warn mode admits an over-budget EndpointType with admission warnings", func() {
			warnVal := NewValidator(cost.ModeWarn)
			warnings, err := warnVal.ValidateCreate(ctx, overbudgetType())
			Expect(err).NotTo(HaveOccurred())
			Expect(warnings).To(HaveLen(1))
			Expect(warnings[0]).To(ContainSubstring("CEL cost verdict: OVER BUDGET"))
		})
	})
})
