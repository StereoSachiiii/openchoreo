// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package cost_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"gopkg.in/yaml.v3"
	"k8s.io/apimachinery/pkg/util/validation/field"
	k8syaml "sigs.k8s.io/yaml"

	"github.com/openchoreo/openchoreo/api/v1alpha1"
	"github.com/openchoreo/openchoreo/internal/validation/component"
	"github.com/openchoreo/openchoreo/internal/validation/component/cost"
	resourcevalidation "github.com/openchoreo/openchoreo/internal/validation/resource"
	"github.com/openchoreo/openchoreo/internal/validation/schemautil"
	workflowwebhook "github.com/openchoreo/openchoreo/internal/webhook/workflow"
)

func TestCorpusValidationReport(t *testing.T) {
	samplesDir := "../../../../samples"
	absPath, err := filepath.Abs(samplesDir)
	if err != nil {
		t.Fatalf("failed to resolve samples dir: %v", err)
	}

	var yamlFiles []string
	err = filepath.Walk(absPath, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if strings.HasSuffix(path, ".yaml") || strings.HasSuffix(path, ".yml") {
			yamlFiles = append(yamlFiles, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("failed to walk samples: %v", err)
	}

	totalTemplates := 0
	totalErrors := 0
	totalCostEvaluated := 0

	for _, file := range yamlFiles {
		content, err := os.ReadFile(file)
		if err != nil {
			continue
		}

		docs := strings.Split(string(content), "\n---")
		for _, doc := range docs {
			if strings.TrimSpace(doc) == "" {
				continue
			}

			// Check kind and name
			var header struct {
				Kind     string `yaml:"kind"`
				Metadata struct {
					Name string `yaml:"name"`
				} `yaml:"metadata"`
			}
			if err := yaml.Unmarshal([]byte(doc), &header); err != nil {
				continue
			}

			switch header.Kind {
			case "ComponentType", "ClusterComponentType":
				totalTemplates++
				var ctSpec v1alpha1.ComponentTypeSpec
				if header.Kind == "ComponentType" {
					var ct v1alpha1.ComponentType
					jsonData, err := k8syaml.YAMLToJSON([]byte(doc))
					if err != nil || json.Unmarshal(jsonData, &ct) != nil {
						continue
					}
					ctSpec = ct.Spec
				} else {
					var cct v1alpha1.ClusterComponentType
					jsonData, err := k8syaml.YAMLToJSON([]byte(doc))
					if err != nil || json.Unmarshal(jsonData, &cct) != nil {
						continue
					}
					ctSpec = cct.Spec.ToComponentTypeSpec()
				}

				paramSchema, envConfigSchema, _ := schemautil.ExtractAndValidateSchemas(
					ctSpec.Parameters, ctSpec.EnvironmentConfigs, field.NewPath("spec"),
				)

				// 1. Template validation
				errs := component.ValidateResourcesWithSchema(
					ctSpec.Resources,
					ctSpec.Validations,
					ctSpec.PreRenderValidations,
					ctSpec.PostRenderValidations,
					paramSchema,
					envConfigSchema,
					field.NewPath("spec"),
				)
				if len(errs) > 0 {
					totalErrors += len(errs)
					t.Errorf("[%s %s] has %d validation errors", header.Kind, header.Metadata.Name, len(errs))
				}

				// 2. Cost estimation: assert no UNKNOWN and no OVER BUDGET on core shipped samples
				val, err := component.NewCELValidator(component.ComponentTypeResource, component.SchemaOptions{
					ParametersSchema:         paramSchema,
					EnvironmentConfigsSchema: envConfigSchema,
				})
				if err != nil {
					t.Errorf("[%s %s] failed to build CEL validator: %v", header.Kind, header.Metadata.Name, err)
					continue
				}

				aggregator := cost.NewTemplateAggregator(val)
				tEst, err := aggregator.EstimateComponentTypeSpec(ctSpec)
				assert.NoError(t, err, "[%s %s] estimation error", header.Kind, header.Metadata.Name)
				if strings.Contains(file, "getting-started") {
					assert.Equal(t, cost.StateKnown, tEst.State, "[%s %s] state must be KNOWN", header.Kind, header.Metadata.Name)
					assert.Equal(t, cost.VerdictWithinBudget, tEst.Verdict, "[%s %s] verdict must be WITHIN BUDGET", header.Kind, header.Metadata.Name)
					assert.LessOrEqual(t, tEst.TotalCost, cost.MaxCumulativeRenderBudget, "[%s %s] cost over budget", header.Kind, header.Metadata.Name)
				} else {
					t.Logf("[%s %s] estimate: cost=%d, state=%s, verdict=%s", header.Kind, header.Metadata.Name, tEst.TotalCost, tEst.State, tEst.Verdict)
				}
				totalCostEvaluated++

			case "Trait", "ClusterTrait":
				totalTemplates++
				var traitSpec v1alpha1.TraitSpec
				if header.Kind == "Trait" {
					var tr v1alpha1.Trait
					jsonData, err := k8syaml.YAMLToJSON([]byte(doc))
					if err != nil || json.Unmarshal(jsonData, &tr) != nil {
						continue
					}
					traitSpec = tr.Spec
				} else {
					var ctr v1alpha1.ClusterTrait
					jsonData, err := k8syaml.YAMLToJSON([]byte(doc))
					if err != nil || json.Unmarshal(jsonData, &ctr) != nil {
						continue
					}
					traitSpec = v1alpha1.TraitSpec(ctr.Spec)
				}

				paramStruct, envConfigStruct, schemaErrs := schemautil.ExtractAndValidateSchemas(
					traitSpec.Parameters, traitSpec.EnvironmentConfigs, field.NewPath("spec"),
				)
				if len(schemaErrs) > 0 {
					totalErrors += len(schemaErrs)
					t.Errorf("[%s %s] schema errors: %v", header.Kind, header.Metadata.Name, schemaErrs)
				}

				errs := component.ValidateTraitSpec(traitSpec, paramStruct, envConfigStruct, field.NewPath("spec"))
				if len(errs) > 0 {
					totalErrors += len(errs)
					t.Errorf("[%s %s] validation errors: %v", header.Kind, header.Metadata.Name, errs)
				}

			case "ResourceType", "ClusterResourceType":
				totalTemplates++
				var errs field.ErrorList
				if header.Kind == "ResourceType" {
					var rt v1alpha1.ResourceType
					jsonData, err := k8syaml.YAMLToJSON([]byte(doc))
					if err != nil || json.Unmarshal(jsonData, &rt) != nil {
						continue
					}
					errs = resourcevalidation.ValidateResourceTypeSpec(&rt.Spec, field.NewPath("spec"))
				} else {
					var crt v1alpha1.ClusterResourceType
					jsonData, err := k8syaml.YAMLToJSON([]byte(doc))
					if err != nil || json.Unmarshal(jsonData, &crt) != nil {
						continue
					}
					errs = resourcevalidation.ValidateClusterResourceTypeSpec(&crt.Spec, field.NewPath("spec"))
				}

				if len(errs) > 0 {
					totalErrors += len(errs)
					t.Errorf("[%s %s] validation errors: %v", header.Kind, header.Metadata.Name, errs)
				}

			case "Workflow", "ClusterWorkflow":
				totalTemplates++
				var wfSpec v1alpha1.WorkflowSpec
				if header.Kind == "Workflow" {
					var wf v1alpha1.Workflow
					jsonData, err := k8syaml.YAMLToJSON([]byte(doc))
					if err != nil || json.Unmarshal(jsonData, &wf) != nil {
						continue
					}
					wfSpec = wf.Spec
				} else {
					var cwf v1alpha1.ClusterWorkflow
					jsonData, err := k8syaml.YAMLToJSON([]byte(doc))
					if err != nil || json.Unmarshal(jsonData, &cwf) != nil {
						continue
					}
					wfSpec = v1alpha1.WorkflowSpec{
						Parameters:   cwf.Spec.Parameters,
						RunTemplate:  cwf.Spec.RunTemplate,
						Resources:    cwf.Spec.Resources,
						ExternalRefs: cwf.Spec.ExternalRefs,
					}
				}

				errs := workflowwebhook.ValidateWorkflowSpec(wfSpec.RunTemplate, wfSpec.Resources, wfSpec.ExternalRefs, wfSpec.Parameters)
				if len(errs) > 0 {
					totalErrors += len(errs)
					t.Errorf("[%s %s] validation errors: %v", header.Kind, header.Metadata.Name, errs)
				}
			}
		}
	}

	t.Logf("=== SUMMARY: Evaluated %d total templates (%d cost-evaluated, %d errors) ===", totalTemplates, totalCostEvaluated, totalErrors)
	assert.Equal(t, 0, totalErrors, "shipped templates must have zero validation errors")
}
