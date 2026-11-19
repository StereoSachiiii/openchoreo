// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package webhook_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	"github.com/openchoreo/openchoreo/api/v1alpha1"
	"github.com/openchoreo/openchoreo/internal/validation/cost"
	"github.com/openchoreo/openchoreo/internal/webhook/clustercomponenttype"
	"github.com/openchoreo/openchoreo/internal/webhook/clusterendpointtype"
	"github.com/openchoreo/openchoreo/internal/webhook/clusterresourcetype"
	"github.com/openchoreo/openchoreo/internal/webhook/clustertrait"
	"github.com/openchoreo/openchoreo/internal/webhook/clusterworkflow"
	"github.com/openchoreo/openchoreo/internal/webhook/componenttype"
	"github.com/openchoreo/openchoreo/internal/webhook/endpointtype"
	"github.com/openchoreo/openchoreo/internal/webhook/resourcetype"
	"github.com/openchoreo/openchoreo/internal/webhook/trait"
	"github.com/openchoreo/openchoreo/internal/webhook/workflow"
)

const webhookQuadExpr = `${string(size(parameters.items.map(a, parameters.items.map(b, a + b))))}`

func webhookSchema(maxItems int64) *v1alpha1.SchemaSection {
	return &v1alpha1.SchemaSection{
		OpenAPIV3Schema: &runtime.RawExtension{
			Raw: []byte(fmt.Sprintf(`{"type":"object","properties":{"items":{"type":"array","maxItems":%d,"items":{"type":"string","maxLength":8}}}}`, maxItems)),
		},
	}
}

func webhookTemplateBody() *runtime.RawExtension {
	return &runtime.RawExtension{
		Raw: []byte(fmt.Sprintf(`{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"cm"},"data":{"val":%q}}`, webhookQuadExpr)),
	}
}

func webhookWorkflowRunTemplateBody() *runtime.RawExtension {
	return &runtime.RawExtension{
		Raw: []byte(fmt.Sprintf(`{"apiVersion":"argoproj.io/v1alpha1","kind":"Workflow","metadata":{"name":"${metadata.workflowRunName}","namespace":"${metadata.namespace}"},"spec":{"arguments":{"parameters":[{"name":"val","value":%q}]}}}`, webhookQuadExpr)),
	}
}

func webhookDeploymentBody() *runtime.RawExtension {
	return &runtime.RawExtension{
		Raw: []byte(fmt.Sprintf(`{"apiVersion":"apps/v1","kind":"Deployment","metadata":{"name":"app","annotations":{"val":%q}}}`, webhookQuadExpr)),
	}
}

type webhookCase struct {
	name         string
	newValidator func(mode cost.Mode, opts ...cost.Option) webhook.CustomValidator
	makeObject   func(sec *v1alpha1.SchemaSection) runtime.Object
}

// TestAllTemplateWebhooks_CostWiringGuardrail verifies every template admission webhook wires
// oldObj schema extraction for ValidateUpdate ratchets and forwards custom celCostLimit options.
func TestAllTemplateWebhooks_CostWiringGuardrail(t *testing.T) {
	secSmall := webhookSchema(5)
	secMid := webhookSchema(300) // ~1.5M units: fails at 1M default, passes at 2M raised limit
	secHuge := webhookSchema(2000)

	cases := []webhookCase{
		{
			name: "ComponentType",
			newValidator: func(mode cost.Mode, opts ...cost.Option) webhook.CustomValidator {
				return componenttype.NewValidator(mode, opts...)
			},
			makeObject: func(sec *v1alpha1.SchemaSection) runtime.Object {
				return &v1alpha1.ComponentType{
					ObjectMeta: metav1.ObjectMeta{Name: "wire-ct", Namespace: "default"},
					Spec: v1alpha1.ComponentTypeSpec{
						WorkloadType: "deployment",
						Parameters:   sec,
						Resources:    []v1alpha1.ResourceTemplate{{ID: "deployment", Template: webhookDeploymentBody()}},
					},
				}
			},
		},
		{
			name: "ClusterComponentType",
			newValidator: func(mode cost.Mode, opts ...cost.Option) webhook.CustomValidator {
				return clustercomponenttype.NewValidator(mode, opts...)
			},
			makeObject: func(sec *v1alpha1.SchemaSection) runtime.Object {
				return &v1alpha1.ClusterComponentType{
					ObjectMeta: metav1.ObjectMeta{Name: "wire-cct"},
					Spec: v1alpha1.ClusterComponentTypeSpec{
						WorkloadType: "deployment",
						Parameters:   sec,
						Resources:    []v1alpha1.ResourceTemplate{{ID: "deployment", Template: webhookDeploymentBody()}},
					},
				}
			},
		},
		{
			name: "Trait",
			newValidator: func(mode cost.Mode, opts ...cost.Option) webhook.CustomValidator {
				return trait.NewValidator(mode, opts...)
			},
			makeObject: func(sec *v1alpha1.SchemaSection) runtime.Object {
				return &v1alpha1.Trait{
					ObjectMeta: metav1.ObjectMeta{Name: "wire-trait", Namespace: "default"},
					Spec: v1alpha1.TraitSpec{
						Parameters: sec,
						Creates:    []v1alpha1.TraitCreate{{Template: webhookTemplateBody()}},
					},
				}
			},
		},
		{
			name: "ClusterTrait",
			newValidator: func(mode cost.Mode, opts ...cost.Option) webhook.CustomValidator {
				return clustertrait.NewValidator(mode, opts...)
			},
			makeObject: func(sec *v1alpha1.SchemaSection) runtime.Object {
				return &v1alpha1.ClusterTrait{
					ObjectMeta: metav1.ObjectMeta{Name: "wire-ctrait"},
					Spec: v1alpha1.ClusterTraitSpec{
						Parameters: sec,
						Creates:    []v1alpha1.TraitCreate{{Template: webhookTemplateBody()}},
					},
				}
			},
		},
		{
			name: "EndpointType",
			newValidator: func(mode cost.Mode, opts ...cost.Option) webhook.CustomValidator {
				return endpointtype.NewValidator(mode, opts...)
			},
			makeObject: func(sec *v1alpha1.SchemaSection) runtime.Object {
				return &v1alpha1.EndpointType{
					ObjectMeta: metav1.ObjectMeta{Name: "wire-et", Namespace: "default"},
					Spec: v1alpha1.EndpointTypeSpec{
						Parameters: sec,
						Resources:  []v1alpha1.ResourceTemplate{{ID: "cm", Template: webhookTemplateBody()}},
					},
				}
			},
		},
		{
			name: "ClusterEndpointType",
			newValidator: func(mode cost.Mode, opts ...cost.Option) webhook.CustomValidator {
				return clusterendpointtype.NewValidator(mode, opts...)
			},
			makeObject: func(sec *v1alpha1.SchemaSection) runtime.Object {
				return &v1alpha1.ClusterEndpointType{
					ObjectMeta: metav1.ObjectMeta{Name: "wire-cet"},
					Spec: v1alpha1.ClusterEndpointTypeSpec{
						Parameters: sec,
						Resources:  []v1alpha1.ResourceTemplate{{ID: "cm", Template: webhookTemplateBody()}},
					},
				}
			},
		},
		{
			name: "ResourceType",
			newValidator: func(mode cost.Mode, opts ...cost.Option) webhook.CustomValidator {
				return resourcetype.NewValidator(mode, opts...)
			},
			makeObject: func(sec *v1alpha1.SchemaSection) runtime.Object {
				return &v1alpha1.ResourceType{
					ObjectMeta: metav1.ObjectMeta{Name: "wire-rt", Namespace: "default"},
					Spec: v1alpha1.ResourceTypeSpec{
						Parameters: sec,
						Resources:  []v1alpha1.ResourceTypeManifest{{ID: "cm", Template: webhookTemplateBody()}},
					},
				}
			},
		},
		{
			name: "ClusterResourceType",
			newValidator: func(mode cost.Mode, opts ...cost.Option) webhook.CustomValidator {
				return clusterresourcetype.NewValidator(mode, opts...)
			},
			makeObject: func(sec *v1alpha1.SchemaSection) runtime.Object {
				return &v1alpha1.ClusterResourceType{
					ObjectMeta: metav1.ObjectMeta{Name: "wire-crt"},
					Spec: v1alpha1.ClusterResourceTypeSpec{
						Parameters: sec,
						Resources:  []v1alpha1.ResourceTypeManifest{{ID: "cm", Template: webhookTemplateBody()}},
					},
				}
			},
		},
		{
			name: "Workflow",
			newValidator: func(mode cost.Mode, opts ...cost.Option) webhook.CustomValidator {
				return workflow.NewValidator(mode, opts...)
			},
			makeObject: func(sec *v1alpha1.SchemaSection) runtime.Object {
				return &v1alpha1.Workflow{
					ObjectMeta: metav1.ObjectMeta{Name: "wire-wf", Namespace: "default"},
					Spec: v1alpha1.WorkflowSpec{
						Parameters:  sec,
						RunTemplate: webhookWorkflowRunTemplateBody(),
					},
				}
			},
		},
		{
			name: "ClusterWorkflow",
			newValidator: func(mode cost.Mode, opts ...cost.Option) webhook.CustomValidator {
				return clusterworkflow.NewValidator(mode, opts...)
			},
			makeObject: func(sec *v1alpha1.SchemaSection) runtime.Object {
				return &v1alpha1.ClusterWorkflow{
					ObjectMeta: metav1.ObjectMeta{Name: "wire-cwf"},
					Spec: v1alpha1.ClusterWorkflowSpec{
						Parameters:  sec,
						RunTemplate: webhookWorkflowRunTemplateBody(),
					},
				}
			},
		},
	}

	ctx := context.Background()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defaultVal := tc.newValidator(cost.ModeEnforce)
			oldObj := tc.makeObject(secSmall)
			relaxedObj := tc.makeObject(secHuge)

			_, err := defaultVal.ValidateCreate(ctx, oldObj)
			require.NoError(t, err, "%s with maxItems=5 must pass ValidateCreate", tc.name)

			_, err = defaultVal.ValidateUpdate(ctx, oldObj, relaxedObj)
			require.Error(t, err,
				"%s webhook ratchet bypass: ValidateUpdate must extract oldObj schemas and reject relaxing maxItems from 5 to 2000", tc.name)
			assert.Contains(t, err.Error(), "CEL cost ratchet violated")

			midObj := tc.makeObject(secMid)
			_, err = defaultVal.ValidateCreate(ctx, midObj)
			require.Error(t, err, "%s mid-cost fixture (~1.5M) must fail at default 1M expression limit", tc.name)

			raisedVal := tc.newValidator(cost.ModeEnforce, cost.WithExpressionLimit(2_000_000))
			_, err = raisedVal.ValidateCreate(ctx, midObj)
			assert.NoError(t, err,
				"%s webhook dual-budget drift: NewValidator must forward cost.WithExpressionLimit(2000000) to estimator", tc.name)
		})
	}
}
