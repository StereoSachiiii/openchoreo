// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package validate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	k8syaml "sigs.k8s.io/yaml"

	"github.com/openchoreo/openchoreo/api/v1alpha1"
	componentval "github.com/openchoreo/openchoreo/internal/validation/component"
	"github.com/openchoreo/openchoreo/internal/validation/cost"
	endpointval "github.com/openchoreo/openchoreo/internal/validation/endpoint"
	resourceval "github.com/openchoreo/openchoreo/internal/validation/resource"
	"github.com/openchoreo/openchoreo/internal/validation/schemautil"
	workflowval "github.com/openchoreo/openchoreo/internal/validation/workflow"
)

// Params configures an offline validation run.
type Params struct {
	Path         string
	Mode         cost.Mode
	CELCostLimit uint64
}

type docHeader struct {
	Kind     string `yaml:"kind"`
	Metadata struct {
		Name string `yaml:"name"`
	} `yaml:"metadata"`
}

// Run validates all supported OpenChoreo template CRDs found at params.Path (a single YAML file or directory).
func Run(ctx context.Context, params Params, out io.Writer) error {
	files, err := collectYAMLFiles(params.Path)
	if err != nil {
		return err
	}

	var (
		checked int
		failed  int
	)

	for _, file := range files {
		docs, err := splitYAMLDocuments(file)
		if err != nil {
			failed++
			_, _ = fmt.Fprintf(out, "FAIL %s: %v\n", file, err)
			continue
		}

		for _, doc := range docs {
			var header docHeader
			if err := yaml.Unmarshal(doc, &header); err != nil || header.Kind == "" {
				continue
			}

			obj, schemas, estimator, supported, err := decodeTemplate(doc, header.Kind)
			if !supported {
				continue
			}
			checked++

			label := fmt.Sprintf("%s/%s (%s)", header.Kind, header.Metadata.Name, file)
			if err != nil {
				failed++
				_, _ = fmt.Fprintf(out, "FAIL %s: %v\n", label, err)
				continue
			}

			checker := cost.NewWebhookCostChecker(estimator, params.Mode, cost.WithExpressionLimit(params.CELCostLimit))
			warnings, err := checker.ValidateCreate(ctx, obj, schemas)
			for _, w := range warnings {
				_, _ = fmt.Fprintf(out, "WARN %s: %s\n", label, w)
			}
			if err != nil {
				failed++
				_, _ = fmt.Fprintf(out, "FAIL %s: %v\n", label, err)
				continue
			}
			_, _ = fmt.Fprintf(out, "OK   %s\n", label)
		}
	}

	if checked == 0 {
		_, _ = fmt.Fprintf(out, "No OpenChoreo template manifests found in %s\n", params.Path)
		return nil
	}
	if failed > 0 {
		return fmt.Errorf("validation failed: %d of %d template(s) had errors", failed, checked)
	}
	_, _ = fmt.Fprintf(out, "Validated %d template(s) successfully\n", checked)
	return nil
}

func collectYAMLFiles(root string) ([]string, error) {
	info, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", root, err)
	}
	if !info.IsDir() {
		return []string{root}, nil
	}

	var files []string
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if ext == ".yaml" || ext == ".yml" {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk %s: %w", root, err)
	}
	sort.Strings(files)
	return files, nil
}

func splitYAMLDocuments(file string) ([][]byte, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("read file: %w", err)
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var docs [][]byte
	for {
		var node yaml.Node
		err := dec.Decode(&node)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("parse YAML: %w", err)
		}
		if node.Kind == 0 || len(node.Content) == 0 {
			continue
		}
		raw, err := yaml.Marshal(&node)
		if err != nil {
			return nil, fmt.Errorf("re-encode YAML document: %w", err)
		}
		docs = append(docs, raw)
	}
	return docs, nil
}

func decodeTemplate(doc []byte, kind string) (runtime.Object, cost.SchemaPair, cost.DomainCostEstimator, bool, error) {
	switch kind {
	case "ComponentType":
		return decodeAndExtract(&v1alpha1.ComponentType{}, &componentval.ComponentDomainEstimator{}, doc,
			func(o *v1alpha1.ComponentType) (*v1alpha1.SchemaSection, *v1alpha1.SchemaSection) {
				return o.Spec.Parameters, o.Spec.EnvironmentConfigs
			})
	case "ClusterComponentType":
		return decodeAndExtract(&v1alpha1.ClusterComponentType{}, &componentval.ComponentDomainEstimator{}, doc,
			func(o *v1alpha1.ClusterComponentType) (*v1alpha1.SchemaSection, *v1alpha1.SchemaSection) {
				return o.Spec.Parameters, o.Spec.EnvironmentConfigs
			})
	case "Trait":
		return decodeAndExtract(&v1alpha1.Trait{}, &componentval.TraitDomainEstimator{}, doc,
			func(o *v1alpha1.Trait) (*v1alpha1.SchemaSection, *v1alpha1.SchemaSection) {
				return o.Spec.Parameters, o.Spec.EnvironmentConfigs
			})
	case "ClusterTrait":
		return decodeAndExtract(&v1alpha1.ClusterTrait{}, &componentval.TraitDomainEstimator{}, doc,
			func(o *v1alpha1.ClusterTrait) (*v1alpha1.SchemaSection, *v1alpha1.SchemaSection) {
				return o.Spec.Parameters, o.Spec.EnvironmentConfigs
			})
	case "EndpointType":
		return decodeAndExtract(&v1alpha1.EndpointType{}, &endpointval.EndpointDomainEstimator{}, doc,
			func(o *v1alpha1.EndpointType) (*v1alpha1.SchemaSection, *v1alpha1.SchemaSection) {
				return o.Spec.Parameters, o.Spec.EnvironmentConfigs
			})
	case "ClusterEndpointType":
		return decodeAndExtract(&v1alpha1.ClusterEndpointType{}, &endpointval.EndpointDomainEstimator{}, doc,
			func(o *v1alpha1.ClusterEndpointType) (*v1alpha1.SchemaSection, *v1alpha1.SchemaSection) {
				return o.Spec.Parameters, o.Spec.EnvironmentConfigs
			})
	case "ResourceType":
		return decodeAndExtract(&v1alpha1.ResourceType{}, &resourceval.ResourceDomainEstimator{}, doc,
			func(o *v1alpha1.ResourceType) (*v1alpha1.SchemaSection, *v1alpha1.SchemaSection) {
				return o.Spec.Parameters, o.Spec.EnvironmentConfigs
			})
	case "ClusterResourceType":
		return decodeAndExtract(&v1alpha1.ClusterResourceType{}, &resourceval.ResourceDomainEstimator{}, doc,
			func(o *v1alpha1.ClusterResourceType) (*v1alpha1.SchemaSection, *v1alpha1.SchemaSection) {
				return o.Spec.Parameters, o.Spec.EnvironmentConfigs
			})
	case "Workflow":
		return decodeAndExtract(&v1alpha1.Workflow{}, &workflowval.WorkflowDomainEstimator{}, doc,
			func(o *v1alpha1.Workflow) (*v1alpha1.SchemaSection, *v1alpha1.SchemaSection) {
				return o.Spec.Parameters, nil
			})
	case "ClusterWorkflow":
		return decodeAndExtract(&v1alpha1.ClusterWorkflow{}, &workflowval.WorkflowDomainEstimator{}, doc,
			func(o *v1alpha1.ClusterWorkflow) (*v1alpha1.SchemaSection, *v1alpha1.SchemaSection) {
				return o.Spec.Parameters, nil
			})
	default:
		return nil, cost.SchemaPair{}, nil, false, nil
	}
}

func decodeAndExtract[T runtime.Object](
	obj T,
	estimator cost.DomainCostEstimator,
	doc []byte,
	getSchemas func(T) (*v1alpha1.SchemaSection, *v1alpha1.SchemaSection),
) (runtime.Object, cost.SchemaPair, cost.DomainCostEstimator, bool, error) {
	jsonBytes, err := k8syaml.YAMLToJSON(doc)
	if err != nil {
		return nil, cost.SchemaPair{}, nil, true, fmt.Errorf("convert YAML to JSON: %w", err)
	}
	if err := json.Unmarshal(jsonBytes, obj); err != nil {
		return nil, cost.SchemaPair{}, nil, true, fmt.Errorf("unmarshal manifest: %w", err)
	}

	paramSec, envSec := getSchemas(obj)
	paramSchema, envSchema, schemaErrs := schemautil.ExtractAndValidateSchemas(paramSec, envSec, field.NewPath("spec"))
	if len(schemaErrs) > 0 {
		return nil, cost.SchemaPair{}, nil, true, fmt.Errorf("invalid schema: %w", schemaErrs.ToAggregate())
	}

	return obj, cost.SchemaPair{
		Parameters:         paramSchema,
		EnvironmentConfigs: envSchema,
	}, estimator, true, nil
}
