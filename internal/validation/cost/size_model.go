// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package cost

import (
	"fmt"
	"sync"

	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"sigs.k8s.io/yaml"

	crdefs "github.com/openchoreo/openchoreo/config/crd"
	"github.com/openchoreo/openchoreo/internal/limits"
)

// Limit is an upper bound on how large a value can get (items, characters, ...).
// If Bounded is false, Max is meaningless and the size is treated as unlimited.
type Limit struct {
	Max     uint64
	Bounded bool
}

// Unbounded means no trusted upper bound is known.
var Unbounded = Limit{}

// UpTo returns a trusted limit of n. Negative values clamp to 0.
func UpTo(n int64) Limit {
	if n < 0 {
		return Limit{Bounded: true}
	}
	return Limit{Max: uint64(n), Bounded: true}
}

var (
	platformLimitsOnce sync.Once
	platformLimitsMap  map[string]Limit
)

// PlatformLimits holds the static size limits of platform-injected CEL variables.
// CRD-backed limits are dynamically extracted from embedded CRD schemas on first call.
var PlatformLimits = initPlatformLimits()

func initPlatformLimits() map[string]Limit {
	platformLimitsOnce.Do(func() {
		platformLimitsMap = buildPlatformLimits()
	})
	return platformLimitsMap
}

// buildPlatformLimits constructs the complete table of platform variable limits.
func buildPlatformLimits() map[string]Limit {
	table := make(map[string]Limit)

	// 1. Extract CRD-backed bounds from embedded Workload CRD
	workloadCRDBounds, err := extractWorkloadCRDBounds()
	if err != nil {
		panic(fmt.Sprintf("failed to parse embedded Workload CRD: %v", err))
	}
	for k, v := range workloadCRDBounds {
		table[k] = v
	}

	// 2. Synthesized context roots derived from CRD bounds
	// configurations (derived from workload.container.env and workload.container.files)
	envBound := table["workload.container.env"]
	filesBound := table["workload.container.files"]

	table["configurations.configs.envs"] = envBound
	table["configurations.secrets.envs"] = envBound
	table["configurations.configs.files"] = filesBound
	table["configurations.secrets.files"] = filesBound
	table["configurations.toConfigFileList"] = filesBound
	table["configurations.toSecretFileList"] = filesBound

	// Single container invariant
	table["configurations.toConfigEnvsByContainer"] = UpTo(1)
	table["configurations.toSecretEnvsByContainer"] = UpTo(1)
	table["configurations.toContainerEnvFrom"] = UpTo(limits.MaxContainerEnvFromEntries)
	table["configurations.toContainerVolumeMounts"] = UpTo(limits.MaxConfigVolumeMounts)
	table["configurations.toVolumes"] = UpTo(limits.MaxConfigVolumeMounts)

	// dependencies context root (derived from CRD spec.dependencies)
	table["dependencies.items"] = table["workload.dependencies.endpoints"]
	table["dependencies.resources"] = table["workload.dependencies.resources"]
	table["dependencies.envVars"] = UpTo(limits.MaxWorkloadEnvVars)
	table["dependencies.volumeMounts"] = UpTo(limits.MaxWorkloadFileVars)
	table["dependencies.volumes"] = UpTo(limits.MaxWorkloadFileVars)
	table["dependencies.toContainerEnvs"] = UpTo(limits.MaxWorkloadEnvVars)
	table["dependencies.toContainerVolumeMounts"] = UpTo(limits.MaxWorkloadFileVars)
	table["dependencies.toVolumes"] = UpTo(limits.MaxWorkloadFileVars)

	// 3. Derived macro outputs and builder values
	endpointsBound := table["workload.endpoints"]
	table["workload.toServicePorts"] = endpointsBound
	table["derived.servicePorts"] = endpointsBound
	table["derived.endpointResources"] = endpointsBound
	table["derived.endpointResources.@values"] = UpTo(limits.MaxEndpointResourcesPerEp)
	table["derived.configFileList"] = filesBound
	table["derived.secretFileList"] = filesBound
	table["derived.containerEnvFrom"] = UpTo(limits.MaxContainerEnvFromEntries)
	table["derived.configVolumeMounts"] = UpTo(limits.MaxConfigVolumeMounts)
	table["derived.configVolumes"] = UpTo(limits.MaxConfigVolumeMounts)
	table["derived.configEnvs"] = UpTo(limits.MaxEnvsByContainerEntries)
	table["derived.secretEnvs"] = UpTo(limits.MaxEnvsByContainerEntries)
	table["derived.dependencyEnvVars"] = UpTo(limits.MaxWorkloadEnvVars)
	table["derived.dependencyVolumeMounts"] = UpTo(limits.MaxWorkloadFileVars)
	table["derived.dependencyVolumes"] = UpTo(limits.MaxWorkloadFileVars)

	// 4. Metadata context bounds
	table["metadata.name"] = UpTo(limits.MaxKubernetesNameLength)
	table["metadata.namespace"] = UpTo(limits.MaxDNSLabelLength)
	table["metadata.componentName"] = UpTo(limits.MaxKubernetesNameLength)
	table["metadata.projectName"] = UpTo(limits.MaxDNSLabelLength)
	table["metadata.environmentName"] = UpTo(limits.MaxDNSLabelLength)
	table["metadata.componentNamespace"] = UpTo(limits.MaxDNSLabelLength)
	table["metadata.podSelectors"] = UpTo(limits.MaxMetadataPodSelectors)
	table["metadata.labels"] = Unbounded
	table["metadata.annotations"] = Unbounded

	// 5. Gateway hosts
	table["gateway.ingress.external.http.host"] = UpTo(limits.MaxKubernetesNameLength)
	table["gateway.ingress.external.https.host"] = UpTo(limits.MaxKubernetesNameLength)
	table["gateway.ingress.internal.http.host"] = UpTo(limits.MaxKubernetesNameLength)
	table["gateway.ingress.internal.https.host"] = UpTo(limits.MaxKubernetesNameLength)

	return table
}

// extractWorkloadCRDBounds extracts bounds declared on openchoreo.dev_workloads.yaml
func extractWorkloadCRDBounds() (map[string]Limit, error) {
	data, err := crdefs.FS.ReadFile("bases/openchoreo.dev_workloads.yaml")
	if err != nil {
		return nil, fmt.Errorf("reading openchoreo.dev_workloads.yaml: %w", err)
	}

	var crd extv1.CustomResourceDefinition
	if err := yaml.Unmarshal(data, &crd); err != nil {
		return nil, fmt.Errorf("unmarshaling Workload CRD: %w", err)
	}

	if len(crd.Spec.Versions) == 0 || crd.Spec.Versions[0].Schema == nil || crd.Spec.Versions[0].Schema.OpenAPIV3Schema == nil {
		return nil, fmt.Errorf("workload CRD has no openAPIV3Schema")
	}

	specSchema := crd.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties["spec"]
	bounds := make(map[string]Limit)

	// Container bounds
	if container, ok := specSchema.Properties["container"]; ok {
		if img, ok := container.Properties["image"]; ok && img.MaxLength != nil {
			bounds["workload.container.image"] = UpTo(*img.MaxLength)
		}
		if cmd, ok := container.Properties["command"]; ok && cmd.MaxItems != nil {
			bounds["workload.container.command"] = UpTo(*cmd.MaxItems)
		}
		if args, ok := container.Properties["args"]; ok && args.MaxItems != nil {
			bounds["workload.container.args"] = UpTo(*args.MaxItems)
		}
		if env, ok := container.Properties["env"]; ok && env.MaxItems != nil {
			bounds["workload.container.env"] = UpTo(*env.MaxItems)
		}
		if files, ok := container.Properties["files"]; ok && files.MaxItems != nil {
			bounds["workload.container.files"] = UpTo(*files.MaxItems)
		}
	}

	// Endpoints bounds
	if endpoints, ok := specSchema.Properties["endpoints"]; ok {
		if endpoints.MaxProperties != nil {
			bounds["workload.endpoints"] = UpTo(*endpoints.MaxProperties)
		}
		if endpoints.AdditionalProperties != nil && endpoints.AdditionalProperties.Schema != nil {
			epProps := endpoints.AdditionalProperties.Schema.Properties
			if vis, ok := epProps["visibility"]; ok && vis.MaxItems != nil {
				bounds["workload.endpoints.@values.visibility"] = UpTo(*vis.MaxItems)
			}
		}
	}

	// Dependencies bounds
	if deps, ok := specSchema.Properties["dependencies"]; ok {
		if eps, ok := deps.Properties["endpoints"]; ok && eps.MaxItems != nil {
			bounds["workload.dependencies.endpoints"] = UpTo(*eps.MaxItems)
		}
		if res, ok := deps.Properties["resources"]; ok && res.MaxItems != nil {
			bounds["workload.dependencies.resources"] = UpTo(*res.MaxItems)
		}
	}

	return bounds, nil
}
