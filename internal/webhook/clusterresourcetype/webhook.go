// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package clusterresourcetype

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

//nolint:unused // exported via webhook bootstrap; keeps parity with sibling webhooks
var clusterresourcetypelog = logf.Log.WithName("clusterresourcetype-resource")

// SetupClusterResourceTypeWebhookWithManager registers the validating webhook
// for ClusterResourceType in the manager.
func SetupClusterResourceTypeWebhookWithManager(mgr ctrl.Manager) error {
	return SetupClusterResourceTypeWebhookWithManagerAndCostLimit(mgr, 0)
}

// SetupClusterResourceTypeWebhookWithManagerAndCostLimit registers the webhook with a custom per-expression CEL cost limit.
func SetupClusterResourceTypeWebhookWithManagerAndCostLimit(mgr ctrl.Manager, celCostLimit uint64) error {
	return ctrl.NewWebhookManagedBy(mgr, &openchoreodevv1alpha1.ClusterResourceType{}).
		WithCustomValidator(NewValidator(cost.Mode(""), cost.WithExpressionLimit(celCostLimit))).
		Complete()
}

// +kubebuilder:webhook:path=/validate-openchoreo-dev-v1alpha1-clusterresourcetype,mutating=false,failurePolicy=fail,sideEffects=None,groups=openchoreo.dev,resources=clusterresourcetypes,verbs=create;update,versions=v1alpha1,name=vclusterresourcetype-v1alpha1.kb.io,admissionReviewVersions=v1

// Validator validates ClusterResourceType resources.
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

// ValidateCreate runs the cluster-scoped resource-type spec validator.
func (v *Validator) ValidateCreate(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	crt, ok := obj.(*openchoreodevv1alpha1.ClusterResourceType)
	if !ok {
		return nil, fmt.Errorf("expected a ClusterResourceType object but got %T", obj)
	}
	clusterresourcetypelog.Info("Validation for ClusterResourceType upon creation", "name", crt.GetName())

	paramSchema, envConfigSchema, errs := resourcevalidation.ValidateClusterResourceTypeSpecWithSchemas(&crt.Spec, field.NewPath("spec"))
	errs = append(errs, resourcevalidation.ValidateLocalDevAddressesAnnotation(
		crt.Annotations, crt.Spec.Outputs, field.NewPath("metadata", "annotations"))...)
	if len(errs) > 0 {
		return nil, apierrors.NewInvalid(crt.GroupVersionKind().GroupKind(), crt.GetName(), errs)
	}

	return v.getCostChecker().ValidateCreate(ctx, crt, cost.SchemaPair{
		Parameters:         paramSchema,
		EnvironmentConfigs: envConfigSchema,
	})
}

// ValidateUpdate re-runs the validator on the new object.
func (v *Validator) ValidateUpdate(ctx context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	oldCRT, ok := oldObj.(*openchoreodevv1alpha1.ClusterResourceType)
	if !ok {
		return nil, fmt.Errorf("expected a ClusterResourceType object for the oldObj but got %T", oldObj)
	}
	crt, ok := newObj.(*openchoreodevv1alpha1.ClusterResourceType)
	if !ok {
		return nil, fmt.Errorf("expected a ClusterResourceType object for the newObj but got %T", newObj)
	}
	clusterresourcetypelog.Info("Validation for ClusterResourceType upon update", "name", crt.GetName())

	paramSchema, envConfigSchema, errs := resourcevalidation.ValidateClusterResourceTypeSpecWithSchemas(&crt.Spec, field.NewPath("spec"))
	errs = append(errs, resourcevalidation.ValidateLocalDevAddressesAnnotation(
		crt.Annotations, crt.Spec.Outputs, field.NewPath("metadata", "annotations"))...)
	if len(errs) > 0 {
		return nil, apierrors.NewInvalid(crt.GroupVersionKind().GroupKind(), crt.GetName(), errs)
	}

	oldParamSchema, oldEnvSchema := schemautil.ExtractSchemasBestEffort(
		oldCRT.Spec.Parameters, oldCRT.Spec.EnvironmentConfigs,
	)

	return v.getCostChecker().ValidateUpdate(
		ctx,
		oldCRT,
		crt,
		cost.SchemaPair{Parameters: oldParamSchema, EnvironmentConfigs: oldEnvSchema},
		cost.SchemaPair{Parameters: paramSchema, EnvironmentConfigs: envConfigSchema},
	)
}

// ValidateDelete is a no-op. ClusterResourceType deletion is gated by RBAC
// and the runtime impact is bounded by the existing finalizer logic on
// consumers.
func (v *Validator) ValidateDelete(_ context.Context, obj runtime.Object) (admission.Warnings, error) {
	crt, ok := obj.(*openchoreodevv1alpha1.ClusterResourceType)
	if !ok {
		return nil, fmt.Errorf("expected a ClusterResourceType object but got %T", obj)
	}
	clusterresourcetypelog.Info("Validation for ClusterResourceType upon deletion", "name", crt.GetName())
	return nil, nil
}
