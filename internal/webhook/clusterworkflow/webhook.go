// Copyright 2025 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package clusterworkflow

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
	"github.com/openchoreo/openchoreo/internal/validation/schemautil"
	workflowvalidation "github.com/openchoreo/openchoreo/internal/validation/workflow"
	workflowwebhook "github.com/openchoreo/openchoreo/internal/webhook/workflow"
)

// nolint:unused
// log is for logging in this package.
var clusterworkflowlog = logf.Log.WithName("clusterworkflow-resource")

// SetupClusterWorkflowWebhookWithManager registers the webhook for ClusterWorkflow in the manager.
func SetupClusterWorkflowWebhookWithManager(mgr ctrl.Manager) error {
	return SetupClusterWorkflowWebhookWithManagerAndCostLimit(mgr, 0)
}

// SetupClusterWorkflowWebhookWithManagerAndCostLimit registers the webhook with a custom per-expression CEL cost limit.
func SetupClusterWorkflowWebhookWithManagerAndCostLimit(mgr ctrl.Manager, celCostLimit uint64) error {
	return ctrl.NewWebhookManagedBy(mgr, &openchoreodevv1alpha1.ClusterWorkflow{}).
		WithCustomValidator(NewValidator(cost.Mode(""), cost.WithExpressionLimit(celCostLimit))).
		WithCustomDefaulter(&Defaulter{}).
		Complete()
}

// +kubebuilder:webhook:path=/validate-openchoreo-dev-v1alpha1-clusterworkflow,mutating=false,failurePolicy=fail,sideEffects=None,groups=openchoreo.dev,resources=clusterworkflows,verbs=create;update,versions=v1alpha1,name=vclusterworkflow-v1alpha1.kb.io,admissionReviewVersions=v1

// Validator validates ClusterWorkflow resources
// +kubebuilder:object:generate=false
type Validator struct {
	costChecker *cost.WebhookCostChecker
}

// NewValidator constructs a Validator wired with the WorkflowDomainEstimator.
func NewValidator(mode cost.Mode, opts ...cost.Option) *Validator {
	return &Validator{
		costChecker: cost.NewWebhookCostChecker(&workflowvalidation.WorkflowDomainEstimator{}, mode, opts...),
	}
}

func (v *Validator) getCostChecker() *cost.WebhookCostChecker {
	if v.costChecker != nil {
		return v.costChecker
	}
	return NewValidator("").costChecker
}

var _ webhook.CustomValidator = &Validator{}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type ClusterWorkflow.
func (v *Validator) ValidateCreate(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	cwf, ok := obj.(*openchoreodevv1alpha1.ClusterWorkflow)
	if !ok {
		return nil, fmt.Errorf("expected a ClusterWorkflow object but got %T", obj)
	}
	clusterworkflowlog.Info("Validation for ClusterWorkflow upon creation", "name", cwf.GetName())

	paramSchema, allErrs := validateClusterWorkflowWithSchema(cwf)

	if len(allErrs) > 0 {
		return nil, apierrors.NewInvalid(cwf.GroupVersionKind().GroupKind(), cwf.GetName(), allErrs)
	}

	return v.getCostChecker().ValidateCreate(ctx, cwf, cost.SchemaPair{Parameters: paramSchema})
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type ClusterWorkflow.
func (v *Validator) ValidateUpdate(ctx context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	oldCwf, ok := oldObj.(*openchoreodevv1alpha1.ClusterWorkflow)
	if !ok {
		return nil, fmt.Errorf("expected a ClusterWorkflow object for the oldObj but got %T", oldObj)
	}
	newCwf, ok := newObj.(*openchoreodevv1alpha1.ClusterWorkflow)
	if !ok {
		return nil, fmt.Errorf("expected a ClusterWorkflow object for the newObj but got %T", newObj)
	}
	clusterworkflowlog.Info("Validation for ClusterWorkflow upon update", "name", newCwf.GetName())

	paramSchema, allErrs := validateClusterWorkflowWithSchema(newCwf)

	if len(allErrs) > 0 {
		return nil, apierrors.NewInvalid(newCwf.GroupVersionKind().GroupKind(), newCwf.GetName(), allErrs)
	}

	oldParamSchema, _ := schemautil.ExtractSchemasBestEffort(oldCwf.Spec.Parameters, nil)

	return v.getCostChecker().ValidateUpdate(
		ctx,
		oldCwf,
		newCwf,
		cost.SchemaPair{Parameters: oldParamSchema},
		cost.SchemaPair{Parameters: paramSchema},
	)
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type ClusterWorkflow.
// Deletion webhooks are not used for ClusterWorkflow (verbs=create;update only).
func (v *Validator) ValidateDelete(_ context.Context, _ runtime.Object) (admission.Warnings, error) {
	return nil, nil
}

// +kubebuilder:webhook:path=/mutate-openchoreo-dev-v1alpha1-clusterworkflow,mutating=true,failurePolicy=fail,sideEffects=None,groups=openchoreo.dev,resources=clusterworkflows,verbs=create;update,versions=v1alpha1,name=mclusterworkflow-v1alpha1.kb.io,admissionReviewVersions=v1

// Defaulter sets defaults on ClusterWorkflow resources
// +kubebuilder:object:generate=false
type Defaulter struct{}

var _ webhook.CustomDefaulter = &Defaulter{}

// Default implements webhook.CustomDefaulter so a webhook will be registered for the type ClusterWorkflow.
func (d *Defaulter) Default(_ context.Context, obj runtime.Object) error {
	cwf, ok := obj.(*openchoreodevv1alpha1.ClusterWorkflow)
	if !ok {
		return fmt.Errorf("expected a ClusterWorkflow object but got %T", obj)
	}
	clusterworkflowlog.Info("Defaulting for ClusterWorkflow", "name", cwf.GetName())

	return workflowwebhook.InjectServiceAccountName(cwf.Spec.RunTemplate)
}

// validateClusterWorkflowWithSchema performs validation and returns the parameters schema.
func validateClusterWorkflowWithSchema(cwf *openchoreodevv1alpha1.ClusterWorkflow) (*apiextschema.Structural, field.ErrorList) {
	allErrs := field.ErrorList{}

	// ClusterWorkflow scoping constraint: workflowPlaneRef.kind must be ClusterWorkflowPlane
	if cwf.Spec.WorkflowPlaneRef != nil &&
		cwf.Spec.WorkflowPlaneRef.Kind != openchoreodevv1alpha1.ClusterWorkflowPlaneRefKindClusterWorkflowPlane {
		allErrs = append(allErrs, field.Invalid(
			field.NewPath("spec", "workflowPlaneRef", "kind"),
			string(cwf.Spec.WorkflowPlaneRef.Kind),
			"ClusterWorkflow can only reference ClusterWorkflowPlane, not namespace-scoped WorkflowPlane",
		))
	}

	// Reuse shared validation logic
	paramSchema, sharedErrs := workflowwebhook.ValidateWorkflowSpecWithSchema(
		cwf.Spec.RunTemplate, cwf.Spec.Resources, cwf.Spec.ExternalRefs, cwf.Spec.Parameters,
	)
	allErrs = append(allErrs, sharedErrs...)

	return paramSchema, allErrs
}
