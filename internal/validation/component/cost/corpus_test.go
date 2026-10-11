// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package cost_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
	apiextschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"

	"github.com/openchoreo/openchoreo/api/v1alpha1"
	"github.com/openchoreo/openchoreo/internal/schema"
	"github.com/openchoreo/openchoreo/internal/validation/component"
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

	for _, file := range yamlFiles {
		content, err := os.ReadFile(file)
		if err != nil {
			continue
		}

		docs := strings.Split(string(content), "\n---")
		for _, doc := range docs {
			var node yaml.Node
			if err := yaml.Unmarshal([]byte(doc), &node); err != nil {
				continue
			}

			// Check kind
			var header struct {
				Kind     string `yaml:"kind"`
				Metadata struct {
					Name string `yaml:"name"`
				} `yaml:"metadata"`
			}
			if err := yaml.Unmarshal([]byte(doc), &header); err != nil {
				continue
			}

			if header.Kind == "ComponentType" || header.Kind == "ClusterComponentType" {
				var ct v1alpha1.ComponentType
				if err := yaml.Unmarshal([]byte(doc), &ct); err != nil {
					continue
				}

				totalTemplates++
				var paramStruct *apiextschema.Structural
				if ct.Spec.Parameters != nil {
					paramStruct, _ = schema.ResolveSectionToStructural(ct.Spec.Parameters)
				}

				var envConfigStruct *apiextschema.Structural
				if ct.Spec.EnvironmentConfigs != nil {
					envConfigStruct, _ = schema.ResolveSectionToStructural(ct.Spec.EnvironmentConfigs)
				}

				errs := component.ValidateResourcesWithSchema(
					ct.Spec.Resources,
					ct.Spec.Validations,
					ct.Spec.PreRenderValidations,
					ct.Spec.PostRenderValidations,
					paramStruct,
					envConfigStruct,
					field.NewPath("spec"),
				)

				if len(errs) > 0 {
					totalErrors += len(errs)
					t.Logf("[%s %s] has %d validation errors:", header.Kind, header.Metadata.Name, len(errs))
					for _, e := range errs {
						t.Logf("  - %v", e)
					}
				} else {
					t.Logf("[%s %s] passed all template & CEL checks cleanly", header.Kind, header.Metadata.Name)
				}
			}
		}
	}

	t.Logf("=== SUMMARY: Evaluated %d ComponentTypes with %d total errors ===", totalTemplates, totalErrors)
}
