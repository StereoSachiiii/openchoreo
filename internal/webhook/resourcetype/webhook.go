// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package resourcetype

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	openchoreodevv1alpha1 "github.com/openchoreo/openchoreo/api/v1alpha1"
	"github.com/openchoreo/openchoreo/internal/validation/cost"
	resourcevalidation "github.com/openchoreo/openchoreo/internal/validation/resource"
	"github.com/openchoreo/openchoreo/internal/validation/schemautil"
)

// log is for logging in this package.
//
//nolint:unused // exported via webhook bootstrap; keeps parity with sibling webhooks
var resourcetypelog = logf.Log.WithName("resourcetype-resource")

// SetupResourceTypeWebhookWithManager registers the validating webhook for
// ResourceType in the manager.
func SetupResourceTypeWebhookWithManager(mgr ctrl.Manager) error {
	return SetupResourceTypeWebhookWithManagerAndCostLimit(mgr, 0)
}

// SetupResourceTypeWebhookWithManagerAndCostLimit registers the webhook with a custom per-expression CEL cost limit.
func SetupResourceTypeWebhookWithManagerAndCostLimit(mgr ctrl.Manager, celCostLimit uint64) error {
	return ctrl.NewWebhookManagedBy(mgr, &openchoreodevv1alpha1.ResourceType{}).
		WithCustomValidator(NewValidator(cost.Mode(""), cost.WithExpressionLimit(celCostLimit))).
		Complete()
}

// +kubebuilder:webhook:path=/validate-openchoreo-dev-v1alpha1-resourcetype,mutating=false,failurePolicy=fail,sideEffects=None,groups=openchoreo.dev,resources=resourcetypes,verbs=create;update,versions=v1alpha1,name=vresourcetype-v1alpha1.kb.io,admissionReviewVersions=v1

// Validator validates ResourceType resources.
// +kubebuilder:object:generate=false
type Validator struct {
	costChecker *cost.WebhookCostChecker
}

// NewValidator constructs a Validator wired with the ResourceDomainEstimator.
func NewValidator(mode cost.Mode, opts ...cost.Option) *Validator {
	return &Validator{
		costChecker: cost.NewWebhookCostChecker(&resourcevalidation.ResourceDomainEstimator{}, mode, opts...),
	}
}

func (v *Validator) getCostChecker() *cost.WebhookCostChecker {
	if v.costChecker != nil {
		return v.costChecker
	}
	return NewValidator("").costChecker
}

var _ webhook.CustomValidator = &Validator{}

// ValidateCreate runs the resource-type spec validator on creation.
func (v *Validator) ValidateCreate(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	rt, ok := obj.(*openchoreodevv1alpha1.ResourceType)
	if !ok {
		return nil, fmt.Errorf("expected a ResourceType object but got %T", obj)
	}
	resourcetypelog.Info("Validation for ResourceType upon creation", "name", rt.GetName())

	paramSchema, envConfigSchema, errs := resourcevalidation.ValidateResourceTypeSpecWithSchemas(&rt.Spec, field.NewPath("spec"))
	errs = append(errs, resourcevalidation.ValidateLocalDevAddressesAnnotation(
		rt.Annotations, rt.Spec.Outputs, field.NewPath("metadata", "annotations"))...)
	if len(errs) > 0 {
		return nil, apierrors.NewInvalid(rt.GroupVersionKind().GroupKind(), rt.GetName(), errs)
	}

	return v.getCostChecker().ValidateCreate(ctx, rt, cost.SchemaPair{
		Parameters:         paramSchema,
		EnvironmentConfigs: envConfigSchema,
	})
}

// ValidateUpdate re-runs the validator on the new object. Spec immutability
// for individual fields is enforced by CRD CEL markers; the rest of the spec
// is re-validated to catch CEL drift introduced by an update.
func (v *Validator) ValidateUpdate(ctx context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	oldRT, ok := oldObj.(*openchoreodevv1alpha1.ResourceType)
	if !ok {
		return nil, fmt.Errorf("expected a ResourceType object for the oldObj but got %T", oldObj)
	}
	rt, ok := newObj.(*openchoreodevv1alpha1.ResourceType)
	if !ok {
		return nil, fmt.Errorf("expected a ResourceType object for the newObj but got %T", newObj)
	}
	resourcetypelog.Info("Validation for ResourceType upon update", "name", rt.GetName())

	paramSchema, envConfigSchema, errs := resourcevalidation.ValidateResourceTypeSpecWithSchemas(&rt.Spec, field.NewPath("spec"))
	errs = append(errs, resourcevalidation.ValidateLocalDevAddressesAnnotation(
		rt.Annotations, rt.Spec.Outputs, field.NewPath("metadata", "annotations"))...)
	if len(errs) > 0 {
		return nil, apierrors.NewInvalid(rt.GroupVersionKind().GroupKind(), rt.GetName(), errs)
	}

	oldParamSchema, oldEnvSchema := schemautil.ExtractSchemasBestEffort(
		oldRT.Spec.Parameters, oldRT.Spec.EnvironmentConfigs,
	)

	return v.getCostChecker().ValidateUpdate(
		ctx,
		oldRT,
		rt,
		cost.SchemaPair{Parameters: oldParamSchema, EnvironmentConfigs: oldEnvSchema},
		cost.SchemaPair{Parameters: paramSchema, EnvironmentConfigs: envConfigSchema},
	)
}

// ValidateDelete is a no-op. ResourceType deletion is gated by RBAC and the
// runtime impact is bounded by the existing finalizer logic on consumers.
func (v *Validator) ValidateDelete(_ context.Context, obj runtime.Object) (admission.Warnings, error) {
	rt, ok := obj.(*openchoreodevv1alpha1.ResourceType)
	if !ok {
		return nil, fmt.Errorf("expected a ResourceType object but got %T", obj)
	}
	resourcetypelog.Info("Validation for ResourceType upon deletion", "name", rt.GetName())
	return nil, nil
}
