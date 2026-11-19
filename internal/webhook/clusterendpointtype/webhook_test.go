// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package clusterendpointtype

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

var _ = Describe("ClusterEndpointType Webhook", func() {
	var (
		ctx       context.Context
		validator Validator
	)

	BeforeEach(func() {
		ctx = context.Background()
		validator = Validator{}
	})

	newType := func(tmpl openchoreodevv1alpha1.ResourceTemplate) *openchoreodevv1alpha1.ClusterEndpointType {
		return &openchoreodevv1alpha1.ClusterEndpointType{
			Spec: openchoreodevv1alpha1.ClusterEndpointTypeSpec{Resources: []openchoreodevv1alpha1.ResourceTemplate{tmpl}},
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
		_, err := validator.ValidateCreate(ctx, &openchoreodevv1alpha1.EndpointType{})
		Expect(err).To(HaveOccurred())
	})

	Context("CEL Cost Enforcement & Ratchet", func() {
		makeOverbudgetCET := func(mapsCount int) *openchoreodevv1alpha1.ClusterEndpointType {
			expr := "${parameters.hosts.map(h1, parameters.hosts.map(h2, parameters.hosts.map(h3, h1 + h2 + h3))).size()}"
			if mapsCount == 2 {
				expr = "${parameters.hosts.map(h1, parameters.hosts.map(h2, h1 + h2)).size()}"
			}
			return &openchoreodevv1alpha1.ClusterEndpointType{
				Spec: openchoreodevv1alpha1.ClusterEndpointTypeSpec{
					Parameters: &openchoreodevv1alpha1.SchemaSection{
						OpenAPIV3Schema: &runtime.RawExtension{
							Raw: []byte(`{"type":"object","properties":{"hosts":{"type":"array","maxItems":60,"items":{"type":"string"}}}}`),
						},
					},
					Resources: []openchoreodevv1alpha1.ResourceTemplate{
						{
							ID: "route",
							Template: &runtime.RawExtension{
								Raw: []byte(`{"apiVersion":"gateway.networking.k8s.io/v1","kind":"HTTPRoute","metadata":{"name":"${metadata.name}"},"spec":{"hostnames":["` + expr + `"]}}`),
							},
						},
					},
				},
			}
		}

		It("enforce mode rejects an over-budget ClusterEndpointType on create", func() {
			enforceVal := NewValidator(cost.ModeEnforce)
			warnings, err := enforceVal.ValidateCreate(ctx, makeOverbudgetCET(3))
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("CEL cost verdict: OVER BUDGET"))
			Expect(warnings).To(BeEmpty())
		})

		It("enforce mode allows decreasing cost on update via non-increasing ratchet", func() {
			enforceVal := NewValidator(cost.ModeEnforce)
			oldObj := makeOverbudgetCET(3)
			newObj := makeOverbudgetCET(2) // lower cost
			warnings, err := enforceVal.ValidateUpdate(ctx, oldObj, newObj)
			Expect(err).NotTo(HaveOccurred())
			Expect(warnings).To(HaveLen(1))
			Expect(warnings[0]).To(ContainSubstring("allowed by ratchet"))
		})

		It("enforce mode rejects increasing cost on update violating ratchet", func() {
			enforceVal := NewValidator(cost.ModeEnforce)
			oldObj := makeOverbudgetCET(2)
			newObj := makeOverbudgetCET(3) // higher cost
			warnings, err := enforceVal.ValidateUpdate(ctx, oldObj, newObj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("CEL cost ratchet violated"))
			Expect(warnings).To(BeEmpty())
		})
	})
})
