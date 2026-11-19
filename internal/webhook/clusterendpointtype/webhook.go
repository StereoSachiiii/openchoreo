// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package clusterendpointtype

import (
	"context"
	"fmt"

	apiextschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	openchoreodevv1alpha1 "github.com/openchoreo/openchoreo/api/v1alpha1"
	"github.com/openchoreo/openchoreo/internal/validation/cost"
	endpointvalidation "github.com/openchoreo/openchoreo/internal/validation/endpoint"
	"github.com/openchoreo/openchoreo/internal/validation/schemautil"
)

var clusterendpointtypelog = logf.Log.WithName("clusterendpointtype-resource")

// SetupClusterEndpointTypeWebhookWithManager registers the validating webhook
// for ClusterEndpointType in the manager.
func SetupClusterEndpointTypeWebhookWithManager(mgr ctrl.Manager) error {
	return SetupClusterEndpointTypeWebhookWithManagerAndCostLimit(mgr, 0)
}

// SetupClusterEndpointTypeWebhookWithManagerAndCostLimit registers the webhook with a custom per-expression CEL cost limit.
func SetupClusterEndpointTypeWebhookWithManagerAndCostLimit(mgr ctrl.Manager, celCostLimit uint64) error {
	return ctrl.NewWebhookManagedBy(mgr, &openchoreodevv1alpha1.ClusterEndpointType{}).
		WithCustomValidator(NewValidator(cost.Mode(""), cost.WithExpressionLimit(celCostLimit))).
		Complete()
}

// +kubebuilder:webhook:path=/validate-openchoreo-dev-v1alpha1-clusterendpointtype,mutating=false,failurePolicy=fail,sideEffects=None,groups=openchoreo.dev,resources=clusterendpointtypes,verbs=create;update,versions=v1alpha1,name=vclusterendpointtype-v1alpha1.kb.io,admissionReviewVersions=v1

// Validator validates ClusterEndpointType resources.
// +kubebuilder:object:generate=false
type Validator struct {
	costChecker *cost.WebhookCostChecker
}

// NewValidator constructs a Validator wired with the EndpointDomainEstimator.
func NewValidator(mode cost.Mode, opts ...cost.Option) *Validator {
	return &Validator{
		costChecker: cost.NewWebhookCostChecker(&endpointvalidation.EndpointDomainEstimator{}, mode, opts...),
	}
}

func (v *Validator) getCostChecker() *cost.WebhookCostChecker {
	if v.costChecker != nil {
		return v.costChecker
	}
	return NewValidator("").costChecker
}

var _ webhook.CustomValidator = &Validator{}

// ValidateCreate runs the endpoint-type spec validator on creation.
func (v *Validator) ValidateCreate(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	cet, ok := obj.(*openchoreodevv1alpha1.ClusterEndpointType)
	if !ok {
		return nil, fmt.Errorf("expected a ClusterEndpointType object but got %T", obj)
	}
	clusterendpointtypelog.Info("Validation for ClusterEndpointType upon creation", "name", cet.GetName())

	paramSchema, envConfigSchema, err := validate(cet)
	if err != nil {
		return nil, err
	}

	return v.getCostChecker().ValidateCreate(ctx, cet, cost.SchemaPair{
		Parameters:         paramSchema,
		EnvironmentConfigs: envConfigSchema,
	})
}

// ValidateUpdate re-runs the validator on the new object. Endpoints pin a
// snapshot of the type in their EndpointRelease, so an update affects only
// releases cut after it; validating it here keeps those releases renderable.
func (v *Validator) ValidateUpdate(ctx context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	oldCET, ok := oldObj.(*openchoreodevv1alpha1.ClusterEndpointType)
	if !ok {
		return nil, fmt.Errorf("expected a ClusterEndpointType object for the oldObj but got %T", oldObj)
	}
	cet, ok := newObj.(*openchoreodevv1alpha1.ClusterEndpointType)
	if !ok {
		return nil, fmt.Errorf("expected a ClusterEndpointType object for the newObj but got %T", newObj)
	}
	clusterendpointtypelog.Info("Validation for ClusterEndpointType upon update", "name", cet.GetName())

	paramSchema, envConfigSchema, err := validate(cet)
	if err != nil {
		return nil, err
	}

	oldParamSchema, oldEnvSchema := schemautil.ExtractSchemasBestEffort(
		oldCET.Spec.Parameters, oldCET.Spec.EnvironmentConfigs,
	)

	return v.getCostChecker().ValidateUpdate(
		ctx,
		oldCET,
		cet,
		cost.SchemaPair{Parameters: oldParamSchema, EnvironmentConfigs: oldEnvSchema},
		cost.SchemaPair{Parameters: paramSchema, EnvironmentConfigs: envConfigSchema},
	)
}

// ValidateDelete is a no-op. Existing Endpoints keep rendering from the
// snapshot pinned in their EndpointRelease.
func (v *Validator) ValidateDelete(_ context.Context, obj runtime.Object) (admission.Warnings, error) {
	if _, ok := obj.(*openchoreodevv1alpha1.ClusterEndpointType); !ok {
		return nil, fmt.Errorf("expected a ClusterEndpointType object but got %T", obj)
	}
	return nil, nil
}

func validate(cet *openchoreodevv1alpha1.ClusterEndpointType) (*apiextschema.Structural, *apiextschema.Structural, error) {
	paramSchema, envConfigSchema, schemaErrs := schemautil.ExtractAndValidateSchemas(
		cet.Spec.Parameters, cet.Spec.EnvironmentConfigs, field.NewPath("spec"),
	)
	if len(schemaErrs) > 0 {
		return nil, nil, apierrors.NewInvalid(cet.GroupVersionKind().GroupKind(), cet.GetName(), schemaErrs)
	}

	if errs := endpointvalidation.ValidateClusterEndpointTypeSpec(&cet.Spec, field.NewPath("spec")); len(errs) > 0 {
		return nil, nil, apierrors.NewInvalid(cet.GroupVersionKind().GroupKind(), cet.GetName(), errs)
	}
	return paramSchema, envConfigSchema, nil
}
