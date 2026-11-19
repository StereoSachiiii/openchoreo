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
	"github.com/openchoreo/openchoreo/internal/validation/cost"
	resourcevalidation "github.com/openchoreo/openchoreo/internal/validation/resource"
	"github.com/openchoreo/openchoreo/internal/validation/schemautil"
	workflowvalidation "github.com/openchoreo/openchoreo/internal/validation/workflow"
	workflowwebhook "github.com/openchoreo/openchoreo/internal/webhook/workflow"
)

type corpusStats struct {
	totalTemplates     int
	totalErrors        int
	totalCostEvaluated int
}

type corpusDocHeader struct {
	Kind     string `yaml:"kind"`
	Metadata struct {
		Name string `yaml:"name"`
	} `yaml:"metadata"`
}

func TestCorpusValidationReport(t *testing.T) {
	samplesDir := "../../../samples"
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

	var stats corpusStats
	for _, file := range yamlFiles {
		content, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		for _, doc := range strings.Split(string(content), "\n---") {
			if strings.TrimSpace(doc) == "" {
				continue
			}
			evaluateCorpusDoc(t, file, doc, &stats)
		}
	}

	t.Logf("=== SUMMARY: Evaluated %d total templates (%d cost-evaluated, %d errors) ===",
		stats.totalTemplates, stats.totalCostEvaluated, stats.totalErrors)
	assert.Equal(t, 0, stats.totalErrors, "shipped templates must have zero validation errors")
}

func evaluateCorpusDoc(t *testing.T, file, doc string, stats *corpusStats) {
	t.Helper()
	var header corpusDocHeader
	if err := yaml.Unmarshal([]byte(doc), &header); err != nil {
		return
	}

	switch header.Kind {
	case kindComponentType, "ClusterComponentType":
		evaluateCorpusComponentType(t, file, doc, header, stats)
	case "Trait", "ClusterTrait":
		evaluateCorpusTrait(t, doc, header, stats)
	case "ResourceType", "ClusterResourceType":
		evaluateCorpusResourceType(t, doc, header, stats)
	case "Workflow", "ClusterWorkflow":
		evaluateCorpusWorkflow(t, doc, header, stats)
	}
}

func evaluateCorpusComponentType(t *testing.T, file, doc string, header corpusDocHeader, stats *corpusStats) {
	t.Helper()
	stats.totalTemplates++
	var ctSpec v1alpha1.ComponentTypeSpec
	jsonData, err := k8syaml.YAMLToJSON([]byte(doc))
	if err != nil {
		return
	}
	if header.Kind == kindComponentType {
		var ct v1alpha1.ComponentType
		if json.Unmarshal(jsonData, &ct) != nil {
			return
		}
		ctSpec = ct.Spec
	} else {
		var cct v1alpha1.ClusterComponentType
		if json.Unmarshal(jsonData, &cct) != nil {
			return
		}
		ctSpec = cct.Spec.ToComponentTypeSpec()
	}

	paramSchema, envConfigSchema, _ := schemautil.ExtractAndValidateSchemas(
		ctSpec.Parameters, ctSpec.EnvironmentConfigs, field.NewPath("spec"),
	)

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
		stats.totalErrors += len(errs)
		t.Errorf("[%s %s] has %d validation errors", header.Kind, header.Metadata.Name, len(errs))
	}

	tEst, err := component.EstimateComponentTypeSpec(ctSpec, paramSchema, envConfigSchema)
	assert.NoError(t, err, "[%s %s] estimation error", header.Kind, header.Metadata.Name)
	if strings.Contains(file, "getting-started") {
		assert.Equal(t, cost.StateKnown, tEst.State, "[%s %s] state must be KNOWN", header.Kind, header.Metadata.Name)
		assert.Equal(t, cost.VerdictWithinBudget, tEst.Verdict, "[%s %s] verdict must be WITHIN BUDGET", header.Kind, header.Metadata.Name)
		assert.LessOrEqual(t, tEst.TotalCost, cost.MaxCumulativeRenderBudget, "[%s %s] cost over budget", header.Kind, header.Metadata.Name)
	} else {
		t.Logf("[%s %s] estimate: cost=%d, state=%s, verdict=%s", header.Kind, header.Metadata.Name, tEst.TotalCost, tEst.State, tEst.Verdict)
	}
	stats.totalCostEvaluated++
}

func evaluateCorpusTrait(t *testing.T, doc string, header corpusDocHeader, stats *corpusStats) {
	t.Helper()
	stats.totalTemplates++
	jsonData, err := k8syaml.YAMLToJSON([]byte(doc))
	if err != nil {
		return
	}
	var traitSpec v1alpha1.TraitSpec
	if header.Kind == "Trait" {
		var tr v1alpha1.Trait
		if json.Unmarshal(jsonData, &tr) != nil {
			return
		}
		traitSpec = tr.Spec
	} else {
		var ctr v1alpha1.ClusterTrait
		if json.Unmarshal(jsonData, &ctr) != nil {
			return
		}
		traitSpec = v1alpha1.TraitSpec(ctr.Spec)
	}

	paramStruct, envConfigStruct, schemaErrs := schemautil.ExtractAndValidateSchemas(
		traitSpec.Parameters, traitSpec.EnvironmentConfigs, field.NewPath("spec"),
	)
	if len(schemaErrs) > 0 {
		stats.totalErrors += len(schemaErrs)
		t.Errorf("[%s %s] schema errors: %v", header.Kind, header.Metadata.Name, schemaErrs)
	}

	errs := component.ValidateTraitSpec(traitSpec, paramStruct, envConfigStruct, field.NewPath("spec"))
	if len(errs) > 0 {
		stats.totalErrors += len(errs)
		t.Errorf("[%s %s] validation errors: %v", header.Kind, header.Metadata.Name, errs)
	}

	tEst, err := component.EstimateTraitSpec(traitSpec, paramStruct, envConfigStruct)
	if assert.NoError(t, err, "[%s %s] estimation error", header.Kind, header.Metadata.Name) {
		t.Logf("[%s %s] estimate: cost=%d, state=%s, verdict=%s", header.Kind, header.Metadata.Name, tEst.TotalCost, tEst.State, tEst.Verdict)
		stats.totalCostEvaluated++
	}
}

func evaluateCorpusResourceType(t *testing.T, doc string, header corpusDocHeader, stats *corpusStats) {
	t.Helper()
	stats.totalTemplates++
	jsonData, err := k8syaml.YAMLToJSON([]byte(doc))
	if err != nil {
		return
	}
	var errs field.ErrorList
	var rtSpec v1alpha1.ResourceTypeSpec
	if header.Kind == "ResourceType" {
		var rt v1alpha1.ResourceType
		if json.Unmarshal(jsonData, &rt) != nil {
			return
		}
		rtSpec = rt.Spec
		errs = resourcevalidation.ValidateResourceTypeSpec(&rt.Spec, field.NewPath("spec"))
	} else {
		var crt v1alpha1.ClusterResourceType
		if json.Unmarshal(jsonData, &crt) != nil {
			return
		}
		rtSpec = v1alpha1.ResourceTypeSpec{
			Parameters:         crt.Spec.Parameters,
			EnvironmentConfigs: crt.Spec.EnvironmentConfigs,
			Resources:          crt.Spec.Resources,
			Outputs:            crt.Spec.Outputs,
		}
		errs = resourcevalidation.ValidateClusterResourceTypeSpec(&crt.Spec, field.NewPath("spec"))
	}

	if len(errs) > 0 {
		stats.totalErrors += len(errs)
		t.Errorf("[%s %s] validation errors: %v", header.Kind, header.Metadata.Name, errs)
	}

	paramSchema, envConfigSchema, _ := schemautil.ExtractAndValidateSchemas(
		rtSpec.Parameters, rtSpec.EnvironmentConfigs, field.NewPath("spec"),
	)
	if tEst, err := resourcevalidation.EstimateResourceTypeSpec(rtSpec, paramSchema, envConfigSchema); err == nil {
		t.Logf("[%s %s] estimate: cost=%d, state=%s, verdict=%s", header.Kind, header.Metadata.Name, tEst.TotalCost, tEst.State, tEst.Verdict)
		stats.totalCostEvaluated++
	}
}

func evaluateCorpusWorkflow(t *testing.T, doc string, header corpusDocHeader, stats *corpusStats) {
	t.Helper()
	stats.totalTemplates++
	jsonData, err := k8syaml.YAMLToJSON([]byte(doc))
	if err != nil {
		return
	}
	var wfSpec v1alpha1.WorkflowSpec
	if header.Kind == "Workflow" {
		var wf v1alpha1.Workflow
		if json.Unmarshal(jsonData, &wf) != nil {
			return
		}
		wfSpec = wf.Spec
	} else {
		var cwf v1alpha1.ClusterWorkflow
		if json.Unmarshal(jsonData, &cwf) != nil {
			return
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
		stats.totalErrors += len(errs)
		t.Errorf("[%s %s] validation errors: %v", header.Kind, header.Metadata.Name, errs)
	}

	paramSchema, _, _ := schemautil.ExtractAndValidateSchemas(
		wfSpec.Parameters, nil, field.NewPath("spec"),
	)
	if tEst, err := workflowvalidation.EstimateWorkflowSpec(wfSpec, paramSchema); err == nil {
		t.Logf("[%s %s] estimate: cost=%d, state=%s, verdict=%s", header.Kind, header.Metadata.Name, tEst.TotalCost, tEst.State, tEst.Verdict)
		stats.totalCostEvaluated++
	}
}
