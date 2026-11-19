// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package endpointtype

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

var endpointtypelog = logf.Log.WithName("endpointtype-resource")

// SetupEndpointTypeWebhookWithManager registers the validating webhook for
// EndpointType in the manager.
func SetupEndpointTypeWebhookWithManager(mgr ctrl.Manager) error {
	return SetupEndpointTypeWebhookWithManagerAndCostLimit(mgr, 0)
}

// SetupEndpointTypeWebhookWithManagerAndCostLimit registers the webhook with a custom per-expression CEL cost limit.
func SetupEndpointTypeWebhookWithManagerAndCostLimit(mgr ctrl.Manager, celCostLimit uint64) error {
	return ctrl.NewWebhookManagedBy(mgr, &openchoreodevv1alpha1.EndpointType{}).
		WithCustomValidator(NewValidator(cost.Mode(""), cost.WithExpressionLimit(celCostLimit))).
		Complete()
}

// +kubebuilder:webhook:path=/validate-openchoreo-dev-v1alpha1-endpointtype,mutating=false,failurePolicy=fail,sideEffects=None,groups=openchoreo.dev,resources=endpointtypes,verbs=create;update,versions=v1alpha1,name=vendpointtype-v1alpha1.kb.io,admissionReviewVersions=v1

// Validator validates EndpointType resources.
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
	et, ok := obj.(*openchoreodevv1alpha1.EndpointType)
	if !ok {
		return nil, fmt.Errorf("expected an EndpointType object but got %T", obj)
	}
	endpointtypelog.Info("Validation for EndpointType upon creation", "name", et.GetName())

	paramSchema, envConfigSchema, err := validate(et)
	if err != nil {
		return nil, err
	}

	return v.getCostChecker().ValidateCreate(ctx, et, cost.SchemaPair{
		Parameters:         paramSchema,
		EnvironmentConfigs: envConfigSchema,
	})
}

// ValidateUpdate re-runs the validator on the new object. Endpoints pin a
// snapshot of the type in their EndpointRelease, so an update affects only
// releases cut after it; validating it here keeps those releases renderable.
func (v *Validator) ValidateUpdate(ctx context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	oldET, ok := oldObj.(*openchoreodevv1alpha1.EndpointType)
	if !ok {
		return nil, fmt.Errorf("expected an EndpointType object for the oldObj but got %T", oldObj)
	}
	et, ok := newObj.(*openchoreodevv1alpha1.EndpointType)
	if !ok {
		return nil, fmt.Errorf("expected an EndpointType object for the newObj but got %T", newObj)
	}
	endpointtypelog.Info("Validation for EndpointType upon update", "name", et.GetName())

	paramSchema, envConfigSchema, err := validate(et)
	if err != nil {
		return nil, err
	}

	oldParamSchema, oldEnvSchema := schemautil.ExtractSchemasBestEffort(
		oldET.Spec.Parameters, oldET.Spec.EnvironmentConfigs,
	)

	return v.getCostChecker().ValidateUpdate(
		ctx,
		oldET,
		et,
		cost.SchemaPair{Parameters: oldParamSchema, EnvironmentConfigs: oldEnvSchema},
		cost.SchemaPair{Parameters: paramSchema, EnvironmentConfigs: envConfigSchema},
	)
}

// ValidateDelete is a no-op. Existing Endpoints keep rendering from the
// snapshot pinned in their EndpointRelease.
func (v *Validator) ValidateDelete(_ context.Context, obj runtime.Object) (admission.Warnings, error) {
	if _, ok := obj.(*openchoreodevv1alpha1.EndpointType); !ok {
		return nil, fmt.Errorf("expected an EndpointType object but got %T", obj)
	}
	return nil, nil
}

func validate(et *openchoreodevv1alpha1.EndpointType) (*apiextschema.Structural, *apiextschema.Structural, error) {
	paramSchema, envConfigSchema, schemaErrs := schemautil.ExtractAndValidateSchemas(
		et.Spec.Parameters, et.Spec.EnvironmentConfigs, field.NewPath("spec"),
	)
	if len(schemaErrs) > 0 {
		return nil, nil, apierrors.NewInvalid(et.GroupVersionKind().GroupKind(), et.GetName(), schemaErrs)
	}

	if errs := endpointvalidation.ValidateEndpointTypeSpec(&et.Spec, field.NewPath("spec")); len(errs) > 0 {
		return nil, nil, apierrors.NewInvalid(et.GroupVersionKind().GroupKind(), et.GetName(), errs)
	}
	return paramSchema, envConfigSchema, nil
}
