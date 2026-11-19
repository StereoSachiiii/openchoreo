// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package cost_test

import (
	"fmt"
	"testing"

	apiextschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/openchoreo/openchoreo/api/v1alpha1"
	"github.com/openchoreo/openchoreo/internal/validation/component"
	"github.com/openchoreo/openchoreo/internal/validation/cost"
	"github.com/openchoreo/openchoreo/internal/validation/cost/costtest"
	"github.com/openchoreo/openchoreo/internal/validation/endpoint"
	"github.com/openchoreo/openchoreo/internal/validation/resource"
	"github.com/openchoreo/openchoreo/internal/validation/workflow"
)

const contractQuadExpr = `${string(size(parameters.items.map(a, parameters.items.map(b, a + b))))}`

func contractSchema(maxItems *int64) (*v1alpha1.SchemaSection, cost.SchemaPair) {
	var raw string
	var vv *apiextschema.ValueValidation
	if maxItems != nil {
		raw = fmt.Sprintf(`{"type":"object","properties":{"items":{"type":"array","maxItems":%d,"items":{"type":"string","maxLength":8}}}}`, *maxItems)
		v := *maxItems
		vv = &apiextschema.ValueValidation{MaxItems: &v}
	} else {
		raw = `{"type":"object","properties":{"items":{"type":"array","items":{"type":"string","maxLength":8}}}}`
	}
	len8 := int64(8)
	structural := &apiextschema.Structural{
		Generic: apiextschema.Generic{Type: "object"},
		Properties: map[string]apiextschema.Structural{
			"items": {
				Generic:         apiextschema.Generic{Type: "array"},
				ValueValidation: vv,
				Items: &apiextschema.Structural{
					Generic:         apiextschema.Generic{Type: "string"},
					ValueValidation: &apiextschema.ValueValidation{MaxLength: &len8},
				},
			},
		},
	}
	sec := &v1alpha1.SchemaSection{
		OpenAPIV3Schema: &runtime.RawExtension{Raw: []byte(raw)},
	}
	return sec, cost.SchemaPair{Parameters: structural}
}

func contractTemplateBody() *runtime.RawExtension {
	return &runtime.RawExtension{
		Raw: []byte(fmt.Sprintf(`{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"cm"},"data":{"val":%q}}`, contractQuadExpr)),
	}
}

func TestAllDomainCostEstimators_Contract(t *testing.T) {
	nSmall := int64(5)
	nBetween := int64(300)
	nHuge := int64(2000)

	secSmall, pairSmall := contractSchema(&nSmall)
	secBetween, pairBetween := contractSchema(&nBetween)
	secHuge, pairHuge := contractSchema(&nHuge)
	secUnbounded, pairUnbounded := contractSchema(nil)

	unsupported := &v1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "other"}}

	t.Run("ComponentType", func(t *testing.T) {
		mk := func(sec *v1alpha1.SchemaSection) *v1alpha1.ComponentType {
			return &v1alpha1.ComponentType{
				ObjectMeta: metav1.ObjectMeta{Name: "contract-ct", Namespace: "default"},
				Spec: v1alpha1.ComponentTypeSpec{
					WorkloadType: "deployment",
					Parameters:   sec,
					Resources:    []v1alpha1.ResourceTemplate{{ID: "cm", Template: contractTemplateBody()}},
				},
			}
		}
		costtest.RunDomainEstimatorContract(t, costtest.DomainContractSpec{
			Estimator:         &component.ComponentDomainEstimator{},
			WithinBudget:      costtest.ObjectWithSchemas{Object: mk(secSmall), Schemas: pairSmall},
			OverBudget:        costtest.ObjectWithSchemas{Object: mk(secHuge), Schemas: pairHuge},
			UnboundedIterated: costtest.ObjectWithSchemas{Object: mk(secUnbounded), Schemas: pairUnbounded},
			RelaxedSchema:     costtest.ObjectWithSchemas{Object: mk(secHuge), Schemas: pairHuge},
			Between1MAnd2M:    costtest.ObjectWithSchemas{Object: mk(secBetween), Schemas: pairBetween},
			UnsupportedObj:    unsupported,
		})
	})

	t.Run("ClusterComponentType", func(t *testing.T) {
		mk := func(sec *v1alpha1.SchemaSection) *v1alpha1.ClusterComponentType {
			return &v1alpha1.ClusterComponentType{
				ObjectMeta: metav1.ObjectMeta{Name: "contract-cct"},
				Spec: v1alpha1.ClusterComponentTypeSpec{
					WorkloadType: "deployment",
					Parameters:   sec,
					Resources:    []v1alpha1.ResourceTemplate{{ID: "cm", Template: contractTemplateBody()}},
				},
			}
		}
		costtest.RunDomainEstimatorContract(t, costtest.DomainContractSpec{
			Estimator:         &component.ComponentDomainEstimator{},
			WithinBudget:      costtest.ObjectWithSchemas{Object: mk(secSmall), Schemas: pairSmall},
			OverBudget:        costtest.ObjectWithSchemas{Object: mk(secHuge), Schemas: pairHuge},
			UnboundedIterated: costtest.ObjectWithSchemas{Object: mk(secUnbounded), Schemas: pairUnbounded},
			RelaxedSchema:     costtest.ObjectWithSchemas{Object: mk(secHuge), Schemas: pairHuge},
			Between1MAnd2M:    costtest.ObjectWithSchemas{Object: mk(secBetween), Schemas: pairBetween},
			UnsupportedObj:    unsupported,
		})
	})

	t.Run("Trait", func(t *testing.T) {
		mk := func(sec *v1alpha1.SchemaSection) *v1alpha1.Trait {
			return &v1alpha1.Trait{
				ObjectMeta: metav1.ObjectMeta{Name: "contract-trait", Namespace: "default"},
				Spec: v1alpha1.TraitSpec{
					Parameters: sec,
					Creates:    []v1alpha1.TraitCreate{{Template: contractTemplateBody()}},
				},
			}
		}
		costtest.RunDomainEstimatorContract(t, costtest.DomainContractSpec{
			Estimator:         &component.TraitDomainEstimator{},
			WithinBudget:      costtest.ObjectWithSchemas{Object: mk(secSmall), Schemas: pairSmall},
			OverBudget:        costtest.ObjectWithSchemas{Object: mk(secHuge), Schemas: pairHuge},
			UnboundedIterated: costtest.ObjectWithSchemas{Object: mk(secUnbounded), Schemas: pairUnbounded},
			RelaxedSchema:     costtest.ObjectWithSchemas{Object: mk(secHuge), Schemas: pairHuge},
			Between1MAnd2M:    costtest.ObjectWithSchemas{Object: mk(secBetween), Schemas: pairBetween},
			UnsupportedObj:    unsupported,
		})
	})

	t.Run("ClusterTrait", func(t *testing.T) {
		mk := func(sec *v1alpha1.SchemaSection) *v1alpha1.ClusterTrait {
			return &v1alpha1.ClusterTrait{
				ObjectMeta: metav1.ObjectMeta{Name: "contract-ctrait"},
				Spec: v1alpha1.ClusterTraitSpec{
					Parameters: sec,
					Creates:    []v1alpha1.TraitCreate{{Template: contractTemplateBody()}},
				},
			}
		}
		costtest.RunDomainEstimatorContract(t, costtest.DomainContractSpec{
			Estimator:         &component.TraitDomainEstimator{},
			WithinBudget:      costtest.ObjectWithSchemas{Object: mk(secSmall), Schemas: pairSmall},
			OverBudget:        costtest.ObjectWithSchemas{Object: mk(secHuge), Schemas: pairHuge},
			UnboundedIterated: costtest.ObjectWithSchemas{Object: mk(secUnbounded), Schemas: pairUnbounded},
			RelaxedSchema:     costtest.ObjectWithSchemas{Object: mk(secHuge), Schemas: pairHuge},
			Between1MAnd2M:    costtest.ObjectWithSchemas{Object: mk(secBetween), Schemas: pairBetween},
			UnsupportedObj:    unsupported,
		})
	})

	t.Run("EndpointType", func(t *testing.T) {
		mk := func(sec *v1alpha1.SchemaSection) *v1alpha1.EndpointType {
			return &v1alpha1.EndpointType{
				ObjectMeta: metav1.ObjectMeta{Name: "contract-et", Namespace: "default"},
				Spec: v1alpha1.EndpointTypeSpec{
					Parameters: sec,
					Resources:  []v1alpha1.ResourceTemplate{{ID: "cm", Template: contractTemplateBody()}},
				},
			}
		}
		costtest.RunDomainEstimatorContract(t, costtest.DomainContractSpec{
			Estimator:         &endpoint.EndpointDomainEstimator{},
			WithinBudget:      costtest.ObjectWithSchemas{Object: mk(secSmall), Schemas: pairSmall},
			OverBudget:        costtest.ObjectWithSchemas{Object: mk(secHuge), Schemas: pairHuge},
			UnboundedIterated: costtest.ObjectWithSchemas{Object: mk(secUnbounded), Schemas: pairUnbounded},
			RelaxedSchema:     costtest.ObjectWithSchemas{Object: mk(secHuge), Schemas: pairHuge},
			Between1MAnd2M:    costtest.ObjectWithSchemas{Object: mk(secBetween), Schemas: pairBetween},
			UnsupportedObj:    unsupported,
		})
	})

	t.Run("ClusterEndpointType", func(t *testing.T) {
		mk := func(sec *v1alpha1.SchemaSection) *v1alpha1.ClusterEndpointType {
			return &v1alpha1.ClusterEndpointType{
				ObjectMeta: metav1.ObjectMeta{Name: "contract-cet"},
				Spec: v1alpha1.ClusterEndpointTypeSpec{
					Parameters: sec,
					Resources:  []v1alpha1.ResourceTemplate{{ID: "cm", Template: contractTemplateBody()}},
				},
			}
		}
		costtest.RunDomainEstimatorContract(t, costtest.DomainContractSpec{
			Estimator:         &endpoint.EndpointDomainEstimator{},
			WithinBudget:      costtest.ObjectWithSchemas{Object: mk(secSmall), Schemas: pairSmall},
			OverBudget:        costtest.ObjectWithSchemas{Object: mk(secHuge), Schemas: pairHuge},
			UnboundedIterated: costtest.ObjectWithSchemas{Object: mk(secUnbounded), Schemas: pairUnbounded},
			RelaxedSchema:     costtest.ObjectWithSchemas{Object: mk(secHuge), Schemas: pairHuge},
			Between1MAnd2M:    costtest.ObjectWithSchemas{Object: mk(secBetween), Schemas: pairBetween},
			UnsupportedObj:    unsupported,
		})
	})

	t.Run("ResourceType", func(t *testing.T) {
		mk := func(sec *v1alpha1.SchemaSection) *v1alpha1.ResourceType {
			return &v1alpha1.ResourceType{
				ObjectMeta: metav1.ObjectMeta{Name: "contract-rt", Namespace: "default"},
				Spec: v1alpha1.ResourceTypeSpec{
					Parameters: sec,
					Resources:  []v1alpha1.ResourceTypeManifest{{ID: "cm", Template: contractTemplateBody()}},
				},
			}
		}
		costtest.RunDomainEstimatorContract(t, costtest.DomainContractSpec{
			Estimator:         &resource.ResourceDomainEstimator{},
			WithinBudget:      costtest.ObjectWithSchemas{Object: mk(secSmall), Schemas: pairSmall},
			OverBudget:        costtest.ObjectWithSchemas{Object: mk(secHuge), Schemas: pairHuge},
			UnboundedIterated: costtest.ObjectWithSchemas{Object: mk(secUnbounded), Schemas: pairUnbounded},
			RelaxedSchema:     costtest.ObjectWithSchemas{Object: mk(secHuge), Schemas: pairHuge},
			Between1MAnd2M:    costtest.ObjectWithSchemas{Object: mk(secBetween), Schemas: pairBetween},
			UnsupportedObj:    unsupported,
		})
	})

	t.Run("ClusterResourceType", func(t *testing.T) {
		mk := func(sec *v1alpha1.SchemaSection) *v1alpha1.ClusterResourceType {
			return &v1alpha1.ClusterResourceType{
				ObjectMeta: metav1.ObjectMeta{Name: "contract-crt"},
				Spec: v1alpha1.ClusterResourceTypeSpec{
					Parameters: sec,
					Resources:  []v1alpha1.ResourceTypeManifest{{ID: "cm", Template: contractTemplateBody()}},
				},
			}
		}
		costtest.RunDomainEstimatorContract(t, costtest.DomainContractSpec{
			Estimator:         &resource.ResourceDomainEstimator{},
			WithinBudget:      costtest.ObjectWithSchemas{Object: mk(secSmall), Schemas: pairSmall},
			OverBudget:        costtest.ObjectWithSchemas{Object: mk(secHuge), Schemas: pairHuge},
			UnboundedIterated: costtest.ObjectWithSchemas{Object: mk(secUnbounded), Schemas: pairUnbounded},
			RelaxedSchema:     costtest.ObjectWithSchemas{Object: mk(secHuge), Schemas: pairHuge},
			Between1MAnd2M:    costtest.ObjectWithSchemas{Object: mk(secBetween), Schemas: pairBetween},
			UnsupportedObj:    unsupported,
		})
	})

	t.Run("Workflow", func(t *testing.T) {
		mk := func(sec *v1alpha1.SchemaSection) *v1alpha1.Workflow {
			return &v1alpha1.Workflow{
				ObjectMeta: metav1.ObjectMeta{Name: "contract-wf", Namespace: "default"},
				Spec: v1alpha1.WorkflowSpec{
					Parameters:  sec,
					RunTemplate: contractTemplateBody(),
				},
			}
		}
		costtest.RunDomainEstimatorContract(t, costtest.DomainContractSpec{
			Estimator:         &workflow.WorkflowDomainEstimator{},
			WithinBudget:      costtest.ObjectWithSchemas{Object: mk(secSmall), Schemas: pairSmall},
			OverBudget:        costtest.ObjectWithSchemas{Object: mk(secHuge), Schemas: pairHuge},
			UnboundedIterated: costtest.ObjectWithSchemas{Object: mk(secUnbounded), Schemas: pairUnbounded},
			RelaxedSchema:     costtest.ObjectWithSchemas{Object: mk(secHuge), Schemas: pairHuge},
			Between1MAnd2M:    costtest.ObjectWithSchemas{Object: mk(secBetween), Schemas: pairBetween},
			UnsupportedObj:    unsupported,
		})
	})

	t.Run("ClusterWorkflow", func(t *testing.T) {
		mk := func(sec *v1alpha1.SchemaSection) *v1alpha1.ClusterWorkflow {
			return &v1alpha1.ClusterWorkflow{
				ObjectMeta: metav1.ObjectMeta{Name: "contract-cwf"},
				Spec: v1alpha1.ClusterWorkflowSpec{
					Parameters:  sec,
					RunTemplate: contractTemplateBody(),
				},
			}
		}
		costtest.RunDomainEstimatorContract(t, costtest.DomainContractSpec{
			Estimator:         &workflow.WorkflowDomainEstimator{},
			WithinBudget:      costtest.ObjectWithSchemas{Object: mk(secSmall), Schemas: pairSmall},
			OverBudget:        costtest.ObjectWithSchemas{Object: mk(secHuge), Schemas: pairHuge},
			UnboundedIterated: costtest.ObjectWithSchemas{Object: mk(secUnbounded), Schemas: pairUnbounded},
			RelaxedSchema:     costtest.ObjectWithSchemas{Object: mk(secHuge), Schemas: pairHuge},
			Between1MAnd2M:    costtest.ObjectWithSchemas{Object: mk(secBetween), Schemas: pairBetween},
			UnsupportedObj:    unsupported,
		})
	})
}
