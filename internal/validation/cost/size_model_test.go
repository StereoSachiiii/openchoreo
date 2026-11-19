// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package cost_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiextschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	apiservercel "k8s.io/apiserver/pkg/cel"
	k8syaml "sigs.k8s.io/yaml"

	"github.com/openchoreo/openchoreo/api/v1alpha1"
	"github.com/openchoreo/openchoreo/internal/limits"
	workflowpipeline "github.com/openchoreo/openchoreo/internal/pipeline/workflow"
	"github.com/openchoreo/openchoreo/internal/validation/component"
	"github.com/openchoreo/openchoreo/internal/validation/cost"
	"github.com/openchoreo/openchoreo/internal/validation/decltype"
	"github.com/openchoreo/openchoreo/internal/validation/endpoint"
	"github.com/openchoreo/openchoreo/internal/validation/resource"
	"github.com/openchoreo/openchoreo/internal/validation/schemautil"
)

func TestPlatformBounds_SyncWithWorkloadCRD(t *testing.T) {
	// 1. workload.container.env bound = 1024
	assert.Equal(t, cost.UpTo(1024), cost.PlatformLimits["workload.container.env"])

	// 2. workload.container.files bound = 256
	assert.Equal(t, cost.UpTo(256), cost.PlatformLimits["workload.container.files"])

	// 3. workload.endpoints bound = 128
	assert.Equal(t, cost.UpTo(128), cost.PlatformLimits["workload.endpoints"])

	// 4. workload.endpoints.@values.visibility bound = 4
	assert.Equal(t, cost.UpTo(4), cost.PlatformLimits["workload.endpoints.@values.visibility"])

	// 5. configurations.configs.envs & secrets.envs bounds = 1024
	assert.Equal(t, cost.UpTo(1024), cost.PlatformLimits["configurations.configs.envs"])
	assert.Equal(t, cost.UpTo(1024), cost.PlatformLimits["configurations.secrets.envs"])

	// 6. configurations.configs.files & secrets.files bounds = 256
	assert.Equal(t, cost.UpTo(256), cost.PlatformLimits["configurations.configs.files"])
	assert.Equal(t, cost.UpTo(256), cost.PlatformLimits["configurations.secrets.files"])
	assert.Equal(t, cost.UpTo(256), cost.PlatformLimits["configurations.toConfigFileList"])
	assert.Equal(t, cost.UpTo(256), cost.PlatformLimits["configurations.toSecretFileList"])

	// 7. Outer container groupings (single container invariant = 1)
	assert.Equal(t, cost.UpTo(1), cost.PlatformLimits["configurations.toConfigEnvsByContainer"])
	assert.Equal(t, cost.UpTo(1), cost.PlatformLimits["configurations.toSecretEnvsByContainer"])

	// 8. Verify reflect.go bounds match CRD limits and PlatformLimits
	val, err := component.NewCELValidator(component.ComponentTypeResource, component.SchemaOptions{})
	assert.NoError(t, err)
	roots := val.GetRootTypes()
	assert.NotNil(t, roots)

	workloadType := roots["workload"]
	assert.NotNil(t, workloadType)
	endpointsField := workloadType.Fields["endpoints"]
	assert.NotNil(t, endpointsField)
	assert.Equal(t, int64(128), endpointsField.Type.MaxElements)

	configType := roots["configurations"]
	assert.NotNil(t, configType)
	configsField := configType.Fields["configs"]
	assert.NotNil(t, configsField)
	envsField := configsField.Type.Fields["envs"]
	assert.NotNil(t, envsField)
	assert.Equal(t, int64(1024), envsField.Type.MaxElements)
	filesField := configsField.Type.Fields["files"]
	assert.NotNil(t, filesField)
	assert.Equal(t, int64(256), filesField.Type.MaxElements)

	// Verify workload.endpoints value (WorkloadEndpoint / EndpointData) visibility slice limit = 4
	assert.NotNil(t, endpointsField.Type.ElemType)
	visibilityField := endpointsField.Type.ElemType.Fields["visibility"]
	assert.NotNil(t, visibilityField)
	assert.Equal(t, int64(4), visibilityField.Type.MaxElements)

	// 9. Verify workload.container.{image,command,args} match CRD limits
	containerField := workloadType.Fields["container"]
	assert.NotNil(t, containerField)
	assert.Equal(t, int64(1024), containerField.Type.Fields["image"].Type.MaxElements)
	assert.Equal(t, int64(32), containerField.Type.Fields["command"].Type.MaxElements)
	assert.Equal(t, int64(128), containerField.Type.Fields["args"].Type.MaxElements)

	// 10. Verify dependencies.* match CRD limits
	depsType := roots["dependencies"]
	assert.NotNil(t, depsType)
	assert.Equal(t, int64(50), depsType.Fields["items"].Type.MaxElements)
	assert.Equal(t, int64(50), depsType.Fields["resources"].Type.MaxElements)
	assert.Equal(t, int64(1024), depsType.Fields["envVars"].Type.MaxElements)
	assert.Equal(t, int64(256), depsType.Fields["volumeMounts"].Type.MaxElements)
	assert.Equal(t, int64(256), depsType.Fields["volumes"].Type.MaxElements)

	// 11. Verify derived.* match builder limits
	derivedType := roots["derived"]
	assert.NotNil(t, derivedType)
	assert.Equal(t, int64(2), derivedType.Fields["containerEnvFrom"].Type.MaxElements)
	assert.Equal(t, int64(512), derivedType.Fields["configVolumeMounts"].Type.MaxElements)
	assert.Equal(t, int64(512), derivedType.Fields["configVolumes"].Type.MaxElements)
	assert.Equal(t, int64(128), derivedType.Fields["servicePorts"].Type.MaxElements)
	assert.Equal(t, int64(1024), derivedType.Fields["dependencyEnvVars"].Type.MaxElements)
	assert.Equal(t, int64(256), derivedType.Fields["dependencyVolumeMounts"].Type.MaxElements)
	assert.Equal(t, int64(256), derivedType.Fields["dependencyVolumes"].Type.MaxElements)
	assert.Equal(t, int64(128), derivedType.Fields["endpointResources"].Type.MaxElements)
	assert.Equal(t, int64(256), derivedType.Fields["endpointResources"].Type.ElemType.MaxElements)

	// 12. Automated drift check: every bounded struct-backed entry in PlatformLimits must match roots
	for path, limit := range cost.PlatformLimits {
		if !limit.Bounded {
			continue
		}
		segments := strings.Split(path, ".")
		node, ok := roots[segments[0]]
		if !ok || node == nil {
			continue
		}
		walked := true
		for _, seg := range segments[1:] {
			if seg == "@values" || seg == "@items" {
				if node.ElemType == nil {
					walked = false
					break
				}
				node = node.ElemType
				continue
			}
			f, exists := node.Fields[seg]
			if !exists || f == nil || f.Type == nil {
				walked = false
				break
			}
			node = f.Type
		}
		if walked {
			require.GreaterOrEqual(t, node.MaxElements, int64(0), "negative MaxElements for %q", path)
			assert.Equal(t, limit, cost.UpTo(node.MaxElements), "drift between PlatformLimits[%q] and reflected DeclType", path)
		}
	}
}

func TestWorkload_SingleContainerInvariant(t *testing.T) {
	// Guard against silent breakage of the configurations.toConfigEnvsByContainer outer loop bound = 1
	wType := reflect.TypeFor[v1alpha1.WorkloadTemplateSpec]()
	_, hasContainers := wType.FieldByName("Containers")
	assert.False(t, hasContainers, "WorkloadTemplateSpec has grown a Containers slice; update configurations.toConfigEnvsByContainer loop bound!")

	containerField, hasContainer := wType.FieldByName("Container")
	assert.True(t, hasContainer, "WorkloadTemplateSpec must have Container field")
	assert.Equal(t, reflect.TypeFor[v1alpha1.Container](), containerField.Type)
}

func TestStaticCost_QuadraticRejection(t *testing.T) {
	val, err := component.NewCELValidator(component.ComponentTypeResource, component.SchemaOptions{})
	assert.NoError(t, err)

	baseEnv := val.GetBaseEnv()

	// Negative Test: Quadratic loop over configurations.configs.envs (N=1024)
	// Calculated static cost is ~231M > 2,000,000 budget -> VerdictOverBudget
	exprEnv := `configurations.configs.envs.map(a, configurations.configs.envs.map(b, a.name + b.name)).size()`
	_, issues := baseEnv.Compile(exprEnv)
	if issues != nil && issues.Err() != nil {
		t.Logf("Compile error for exprEnv: %v", issues.Err())
	}
	estEnv := component.EstimateExpression(val, exprEnv, baseEnv, "resources[test].template.data")
	t.Logf("estEnv: cost=%d, state=%s, verdict=%s", estEnv.Cost, estEnv.State, estEnv.Verdict)
	assert.Equal(t, cost.VerdictOverBudget, estEnv.Verdict, "quadratic over 1024 env vars must be rejected as OVER budget")
	assert.Greater(t, estEnv.Cost, cost.MaxCumulativeRenderBudget)
}

func TestStaticCost_LinearFilterEndpointsAdmission(t *testing.T) {
	val, err := component.NewCELValidator(component.ComponentTypeResource, component.SchemaOptions{})
	assert.NoError(t, err)

	baseEnv := val.GetBaseEnv()

	// Positive Test: Legitimate linear transformMap with filtering over workload.endpoints (N=128)
	// (This matches the HTTPRoute endpoint filtering pattern used in service.yaml)
	exprEndpoints := `workload.endpoints.transformMap(name, ep, "external" in ep.visibility, ep).size()`
	_, issues2 := baseEnv.Compile(exprEndpoints)
	if issues2 != nil && issues2.Err() != nil {
		t.Logf("Compile error for exprEndpoints: %v", issues2.Err())
	}
	estEndpoints := component.EstimateExpression(val, exprEndpoints, baseEnv, "resources[test].template.data")
	t.Logf("estEndpoints: cost=%d, state=%s, verdict=%s", estEndpoints.Cost, estEndpoints.State, estEndpoints.Verdict)
	assert.Equal(t, cost.VerdictWithinBudget, estEndpoints.Verdict, "linear filter over 128 endpoints must be admitted as WITHIN budget")
	assert.LessOrEqual(t, estEndpoints.Cost, cost.MaxCumulativeRenderBudget)
}

func TestServiceComponentType_StaticCostWithinBudget(t *testing.T) {
	serviceYAMLPath := "../../../samples/getting-started/component-types/service.yaml"
	data, err := os.ReadFile(serviceYAMLPath)
	assert.NoError(t, err)

	var ct v1alpha1.ComponentType
	// Convert YAML to JSON so runtime.RawExtension (Template.Raw) is properly populated
	jsonData, err := k8syaml.YAMLToJSON(data)
	assert.NoError(t, err)
	err = json.Unmarshal(jsonData, &ct)
	assert.NoError(t, err)

	valPath := field.NewPath("spec")
	paramSchema, envConfigSchema, _ := schemautil.ExtractAndValidateSchemas(ct.Spec.Parameters, ct.Spec.EnvironmentConfigs, valPath)

	est, err := component.EstimateComponentTypeSpec(ct.Spec, paramSchema, envConfigSchema)
	assert.NoError(t, err)

	t.Logf("service.yaml overall: TotalCost=%d, ValidationCost=%d, Verdict=%s, State=%s", est.TotalCost, est.ValidationCost, est.Verdict, est.State)
	for _, r := range est.Resources {
		t.Logf("  resource %s: Cost=%d, State=%s, Verdict=%s, LoopBound=%d, ExprCount=%d", r.ID, r.TotalCost, r.State, r.Verdict, r.LoopBound, len(r.Expressions))
		for _, e := range r.Expressions {
			t.Logf("    expr at %s: state=%s, cost=%d, expr=%s", e.FieldPath, e.State, e.Cost, e.Expression)
		}
	}

	// All resources in service.yaml evaluate to StateKnown and VerdictWithinBudget
	assert.Equal(t, cost.VerdictWithinBudget, est.Verdict)
	assert.Equal(t, cost.StateKnown, est.State)
	assert.LessOrEqual(t, est.TotalCost, cost.MaxCumulativeRenderBudget)

	for _, r := range est.Resources {
		assert.Equal(t, cost.StateKnown, r.State, "resource %s must be StateKnown", r.ID)
		assert.Equal(t, cost.VerdictWithinBudget, r.Verdict, "resource %s must be VerdictWithinBudget", r.ID)
		assert.LessOrEqual(t, r.TotalCost, cost.MaxCumulativeRenderBudget, "resource %s cost must be within budget", r.ID)
		assert.Greater(t, r.TotalCost, uint64(0), "resource %s cost must be non-zero", r.ID)
	}

	// Validate with WebhookCostChecker in warn mode (default mode) - admits without warnings
	checkerWarn := cost.NewWebhookCostChecker(&component.ComponentDomainEstimator{}, cost.ModeWarn)
	schemas := cost.SchemaPair{Parameters: paramSchema, EnvironmentConfigs: envConfigSchema}
	warnings, err := checkerWarn.ValidateCreate(context.Background(), &ct, schemas)
	assert.NoError(t, err, "service.yaml must be admitted under default warn mode")
	assert.Empty(t, warnings, "service.yaml produces zero ratchet/UNKNOWN warnings under warn mode")

	// Validate with WebhookCostChecker in enforce mode - must admit cleanly!
	checkerEnforce := cost.NewWebhookCostChecker(&component.ComponentDomainEstimator{}, cost.ModeEnforce)
	warningsEnforce, errEnforce := checkerEnforce.ValidateCreate(context.Background(), &ct, schemas)
	assert.NoError(t, errEnforce, "service.yaml must be admitted under enforce mode")
	assert.Empty(t, warningsEnforce)

	// Verify all getting-started component-types admit cleanly under ModeEnforce with Option A default
	for _, sampleFile := range []string{"service.yaml", "webapp.yaml", "worker.yaml", "scheduled-task.yaml"} {
		samplePath := "../../../samples/getting-started/component-types/" + sampleFile
		rawSample, err := os.ReadFile(samplePath)
		assert.NoError(t, err, sampleFile)
		var sampleCT v1alpha1.ComponentType
		sampleJSON, err := k8syaml.YAMLToJSON(rawSample)
		assert.NoError(t, err, sampleFile)
		assert.NoError(t, json.Unmarshal(sampleJSON, &sampleCT), sampleFile)
		pSchema, eSchema, _ := schemautil.ExtractAndValidateSchemas(sampleCT.Spec.Parameters, sampleCT.Spec.EnvironmentConfigs, valPath)
		w, err := checkerEnforce.ValidateCreate(context.Background(), &sampleCT, cost.SchemaPair{Parameters: pSchema, EnvironmentConfigs: eSchema})
		assert.NoError(t, err, "%s must be admitted under enforce mode with Option A default", sampleFile)
		assert.Empty(t, w, "%s should emit no warnings under enforce mode", sampleFile)
	}
}

func TestStaticCost_GatewayHostnamesWithinBudget(t *testing.T) {
	val, err := component.NewCELValidator(component.ComponentTypeResource, component.SchemaOptions{})
	assert.NoError(t, err)

	baseEnv := val.GetBaseEnv()

	expr := `[gateway.ingress.external.?http, gateway.ingress.external.?https]
          .filter(g, g.hasValue()).map(g, g.value().host).distinct()
          .map(h, metadata.environmentName + "-" + metadata.componentNamespace + "." + h)`

	est := component.EstimateExpression(val, expr, baseEnv, "resources[test].template.spec.hostnames")
	assert.Equal(t, cost.StateKnown, est.State)
	assert.Equal(t, cost.VerdictWithinBudget, est.Verdict)
	assert.LessOrEqual(t, est.Cost, cost.MaxCumulativeRenderBudget)
}

func TestStaticCost_DistinctChainedComprehensionPreservesSize(t *testing.T) {
	val, err := component.NewCELValidator(component.ComponentTypeResource, component.SchemaOptions{})
	assert.NoError(t, err)

	baseEnv := val.GetBaseEnv()

	// Regression guard: distinct() must propagate ResultSize so subsequent comprehensions are StateKnown.
	expr := `["a", "b"].distinct().map(h, h)`
	est := component.EstimateExpression(val, expr, baseEnv, "resources[test].template.data")
	assert.Equal(t, cost.StateKnown, est.State, "comprehensions chained on distinct() must evaluate as StateKnown")
	assert.Equal(t, cost.VerdictWithinBudget, est.Verdict)
	assert.LessOrEqual(t, est.Cost, cost.MaxCumulativeRenderBudget)
}

func TestLoopVariableRenameInvariance(t *testing.T) {
	val, err := component.NewCELValidator(component.ComponentTypeResource, component.SchemaOptions{})
	assert.NoError(t, err)

	// Original variable name: envConfig
	resOriginal := v1alpha1.ResourceTemplate{
		ID:      "env-config",
		ForEach: "${configurations.toConfigEnvsByContainer()}",
		Var:     "envConfig",
		Template: &runtime.RawExtension{
			Raw: []byte(`{"data": "${envConfig.envs.transformMapEntry(index, env, {env.name: env.value})}", "metadata": {"name": "${envConfig.resourceName}"}}`),
		},
	}
	estOriginal, err := component.EstimateResource(val, resOriginal)
	assert.NoError(t, err)

	// Renamed variable name: cfg (arbitrary author choice)
	resRenamed := v1alpha1.ResourceTemplate{
		ID:      "env-config",
		ForEach: "${configurations.toConfigEnvsByContainer()}",
		Var:     "cfg",
		Template: &runtime.RawExtension{
			Raw: []byte(`{"data": "${cfg.envs.transformMapEntry(index, env, {env.name: env.value})}", "metadata": {"name": "${cfg.resourceName}"}}`),
		},
	}
	estRenamed, err := component.EstimateResource(val, resRenamed)
	assert.NoError(t, err)

	assert.Equal(t, cost.StateKnown, estOriginal.State)
	assert.Equal(t, cost.VerdictWithinBudget, estOriginal.Verdict)
	assert.Equal(t, cost.StateKnown, estRenamed.State)
	assert.Equal(t, cost.VerdictWithinBudget, estRenamed.Verdict)
	assert.Equal(t, estOriginal.TotalCost, estRenamed.TotalCost, "renaming loop variable must produce identical cost")
}

func TestAggregator_ForEachPathEdgeCases(t *testing.T) {
	val, err := component.NewCELValidator(component.ComponentTypeResource, component.SchemaOptions{})
	assert.NoError(t, err)

	// Case 1: Method argument containing dots, e.g. foo.bar(nested.arg) -> shape rejected, falls back to UNKNOWN
	resWithDotsInArg := v1alpha1.ResourceTemplate{
		ID:      "dot-arg-loop",
		ForEach: "${configurations.toConfigEnvsByContainer(extra.arg.foo)}",
		Var:     "item",
		Template: &runtime.RawExtension{
			Raw: []byte(`{"metadata": {"name": "test"}}`),
		},
	}
	est1, _ := component.EstimateResource(val, resWithDotsInArg)
	assert.Equal(t, cost.StateUnknown, est1.State, "dots in method args must fall back to UNKNOWN")
	assert.Equal(t, cost.MaxForEachItemsPerLoop, est1.LoopBound)

	// Case 2: Nested parens in method call -> shape rejected, falls back to UNKNOWN
	resNestedParens := v1alpha1.ResourceTemplate{
		ID:      "nested-paren-loop",
		ForEach: "${configurations.toConfigEnvsByContainer(foo())}",
		Var:     "item",
		Template: &runtime.RawExtension{
			Raw: []byte(`{"metadata": {"name": "test"}}`),
		},
	}
	est2, _ := component.EstimateResource(val, resNestedParens)
	assert.Equal(t, cost.StateUnknown, est2.State, "nested parens must fall back to UNKNOWN")
	assert.Equal(t, cost.MaxForEachItemsPerLoop, est2.LoopBound)
}

func TestWebhookCostChecker_DetailedDiagnostics(t *testing.T) {
	checker := cost.NewWebhookCostChecker(&component.ComponentDomainEstimator{}, cost.ModeEnforce)

	// Construct ComponentType with unbounded list parameter
	ct := &v1alpha1.ComponentType{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-diagnostic-unknown",
		},
		Spec: v1alpha1.ComponentTypeSpec{
			WorkloadType: "deployment",
			Resources: []v1alpha1.ResourceTemplate{
				{
					ID: "test-res",
					Template: &runtime.RawExtension{
						Raw: []byte(`{"command": "${parameters.unboundedList.map(a, a + '-suff')}"}`),
					},
				},
			},
		},
	}

	_, err := checker.ValidateCreate(context.Background(), ct, cost.SchemaPair{})
	assert.Error(t, err)
	errMsg := err.Error()

	// Assert that the message identifies the verdict, the offending expression, field path, and hint
	assert.Contains(t, errMsg, `ComponentType "test-diagnostic-unknown" CEL cost verdict: UNKNOWN`)
	assert.Contains(t, errMsg, `Resource "test-res"`)
	assert.Contains(t, errMsg, `parameters.unboundedList.map(a, a + '-suff')`)
	assert.Contains(t, errMsg, `resources[test-res].template.command`)
	assert.Contains(t, errMsg, `add 'maxItems' to array parameters in spec.parameters.openAPIV3Schema`)
}

func TestWebhookCostChecker_ActionableMissingSchemaDiagnostics(t *testing.T) {
	checker := cost.NewWebhookCostChecker(&component.ComponentDomainEstimator{}, cost.ModeEnforce)

	// Array with maxItems: 15, but missing maxLength on its string elements
	// (15^3 = 3,375 iterations * ~825 units per 4096-byte string concat = ~2.78M > 2,000,000 limit)
	fifteen := int64(15)
	paramSchema := &apiextschema.Structural{
		Generic: apiextschema.Generic{Type: "object"},
		Properties: map[string]apiextschema.Structural{
			"items": {
				Generic: apiextschema.Generic{Type: "array"},
				ValueValidation: &apiextschema.ValueValidation{
					MaxItems: &fifteen,
				},
				Items: &apiextschema.Structural{
					Generic: apiextschema.Generic{Type: "string"},
					// Intentionally missing maxLength
				},
			},
		},
	}

	ct := &v1alpha1.ComponentType{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-missing-maxlength",
		},
		Spec: v1alpha1.ComponentTypeSpec{
			WorkloadType: "deployment",
			Resources: []v1alpha1.ResourceTemplate{
				{
					ID: "deployment",
					Template: &runtime.RawExtension{
						Raw: []byte(`{"name": "${parameters.items.map(x, parameters.items.map(y, parameters.items.map(z, x + y + z))).size() > 0 ? 'app' : 'empty'}"}`),
					},
				},
			},
		},
	}

	_, err := checker.ValidateCreate(context.Background(), ct, cost.SchemaPair{Parameters: paramSchema})
	assert.Error(t, err)
	errMsg := err.Error()

	// 1. Should identify missing maxLength on parameters.items[]
	assert.Contains(t, errMsg, `Field "parameters.items[]": missing maxLength`)
	assert.Contains(t, errMsg, `spec.parameters.openAPIV3Schema.properties.items.items`)
	assert.Contains(t, errMsg, `maxLength: 100`)

	// 2. Should NOT complain about maxItems on parameters.items because maxItems: 3 was declared
	assert.NotContains(t, errMsg, `Field "parameters.items": missing maxItems`)
}

func TestWebhookCostChecker_TimeoutBehavior(t *testing.T) {
	ct := &v1alpha1.ComponentType{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-timeout-ct",
		},
		Spec: v1alpha1.ComponentTypeSpec{
			WorkloadType: "deployment",
		},
	}

	// 1. Enforce mode with zero timeout -> fails closed with error
	checkerEnforce := cost.NewWebhookCostChecker(&component.ComponentDomainEstimator{}, cost.ModeEnforce)
	checkerEnforce.Timeout = 0 // Immediate timeout
	warningsEnforce, errEnforce := checkerEnforce.ValidateCreate(context.Background(), ct, cost.SchemaPair{})
	assert.Error(t, errEnforce)
	assert.Contains(t, errEnforce.Error(), "evaluation deadline of 0s exceeded")
	assert.Empty(t, warningsEnforce)

	// 2. Warn mode with zero timeout -> admits with warning
	checkerWarn := cost.NewWebhookCostChecker(&component.ComponentDomainEstimator{}, cost.ModeWarn)
	checkerWarn.Timeout = 0 // Immediate timeout
	warningsWarn, errWarn := checkerWarn.ValidateCreate(context.Background(), ct, cost.SchemaPair{})
	assert.NoError(t, errWarn)
	assert.Contains(t, warningsWarn[0], "evaluation deadline exceeded")
}

func TestValidateTraitCreate_CostEnforcement(t *testing.T) {
	// Construct an over-budget Trait with N=100 cubic patch expression (>2M operations)
	var loopList []int
	for i := 0; i < 100; i++ {
		loopList = append(loopList, i)
	}
	loopBytes, err := json.Marshal(loopList)
	assert.NoError(t, err)

	trait := &v1alpha1.Trait{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-cubic-trait",
		},
		Spec: v1alpha1.TraitSpec{
			Patches: []v1alpha1.TraitPatch{
				{
					Target: v1alpha1.PatchTarget{
						Group:   "apps",
						Version: "v1",
						Kind:    "Deployment",
					},
					Operations: []v1alpha1.JSONPatchOperation{
						{
							Op:   "add",
							Path: "/metadata/annotations/cost",
							Value: &runtime.RawExtension{
								Raw: func() []byte {
									s := fmt.Sprintf("${%s.all(i, %s.all(j, %s.all(k, true)))}", string(loopBytes), string(loopBytes), string(loopBytes))
									b, _ := json.Marshal(s)
									return b
								}(),
							},
						},
					},
				},
			},
		},
	}

	rawEst, rawErr := component.EstimateTraitSpec(trait.Spec, nil, nil)
	t.Logf("rawEst: TotalCost=%d, State=%s, Verdict=%s, rawErr=%v, resCount=%d", rawEst.TotalCost, rawEst.State, rawEst.Verdict, rawErr, len(rawEst.Resources))
	if len(rawEst.Resources) > 0 {
		t.Logf("res[0]: TotalCost=%d, ExprCount=%d", rawEst.Resources[0].TotalCost, len(rawEst.Resources[0].Expressions))
		for _, e := range rawEst.Resources[0].Expressions {
			t.Logf("  expr: %s cost=%d state=%s verdict=%s", e.Expression, e.Cost, e.State, e.Verdict)
		}
	}

	// 1. Enforce mode rejects over-budget Trait
	checkerEnforce := cost.NewWebhookCostChecker(&component.TraitDomainEstimator{}, cost.ModeEnforce)
	warningsEnforce, errEnforce := checkerEnforce.ValidateCreate(context.Background(), trait, cost.SchemaPair{})
	t.Logf("errEnforce: %v, warnings: %v", errEnforce, warningsEnforce)
	if assert.Error(t, errEnforce) {
		assert.Contains(t, errEnforce.Error(), `Trait "test-cubic-trait" CEL cost verdict: OVER BUDGET`)
	}
	assert.Empty(t, warningsEnforce)

	// 2. Warn mode admits with warning
	checkerWarn := cost.NewWebhookCostChecker(&component.TraitDomainEstimator{}, cost.ModeWarn)
	warningsWarn, errWarn := checkerWarn.ValidateCreate(context.Background(), trait, cost.SchemaPair{})
	assert.NoError(t, errWarn)
	assert.NotEmpty(t, warningsWarn)
	assert.Contains(t, warningsWarn[0], `Trait "test-cubic-trait" CEL cost verdict: OVER BUDGET`)
}

// TestPlatformBounds_CRDMarkersMatchLimits fails CI if any CRD-mirrored constant in
// internal/limits/limits.go drifts from the embedded Workload CRD schema.
func TestPlatformBounds_CRDMarkersMatchLimits(t *testing.T) {
	crdToLimit := map[string]int64{
		"workload.container.image":              limits.MaxContainerImageLength,
		"workload.container.command":            limits.MaxContainerCommands,
		"workload.container.args":               limits.MaxContainerArgs,
		"workload.container.env":                limits.MaxWorkloadEnvVars,
		"workload.container.files":              limits.MaxWorkloadFileVars,
		"workload.endpoints":                    limits.MaxWorkloadEndpoints,
		"workload.endpoints.@values.visibility": limits.MaxEndpointVisibilities,
		"workload.dependencies.endpoints":       limits.MaxWorkloadDependencies,
		"workload.dependencies.resources":       limits.MaxWorkloadDependencies,
	}

	for path, expected := range crdToLimit {
		actual, ok := cost.PlatformLimits[path]
		require.Truef(t, ok, "missing CRD-backed PlatformLimits entry %q: check extractWorkloadCRDBounds in size_model.go", path)
		assert.Equalf(t, cost.UpTo(expected), actual,
			"CRD marker drift for %q: api/v1alpha1/workload_types.go and internal/limits/limits.go disagree; update both and run make manifests", path)
	}
}

// TestPlatformBounds_AllReflectedCollectionsBounded walks every non-schema reflected DeclType
// across Component, Endpoint, Resource, and Workflow contexts and fails CI if a new slice or map
// lacks a bound without being explicitly allowlisted as intentionally unbounded.
func TestPlatformBounds_AllReflectedCollectionsBounded(t *testing.T) {
	compVal, err := component.NewCELValidator(component.ComponentTypeResource, component.SchemaOptions{})
	require.NoError(t, err)

	_, _, endpointRoots, err := endpoint.BuildEndpointCELEnv(endpoint.SchemaOptions{})
	require.NoError(t, err)

	_, _, resourceRoots, err := resource.BuildResourceCELEnvWithRoots(resource.SchemaOptions{})
	require.NoError(t, err)

	workflowFields := decltype.ExtractFields(
		reflect.TypeFor[workflowpipeline.WorkflowCELContext](),
		map[string]bool{"parameters": true},
	)
	workflowRoots := make(map[string]*apiservercel.DeclType, len(workflowFields))
	for _, f := range workflowFields {
		workflowRoots[f.Name] = f.DeclType
	}

	// Explicit allowlist of platform collections that are intentionally unbounded
	// (O(1) key lookup allowed; iteration rejected at admission).
	intentionallyUnbounded := map[string]bool{
		"metadata.labels":      true,
		"metadata.annotations": true,
	}

	contexts := map[string]map[string]*apiservercel.DeclType{
		"Component": compVal.GetRootTypes(),
		"Endpoint":  endpointRoots,
		"Resource":  resourceRoots,
		"Workflow":  workflowRoots,
	}

	var checkNode func(t *testing.T, ctxName, path string, dt *apiservercel.DeclType, visited map[*apiservercel.DeclType]bool)
	checkNode = func(t *testing.T, ctxName, path string, dt *apiservercel.DeclType, visited map[*apiservercel.DeclType]bool) {
		if dt == nil || visited[dt] {
			return
		}
		visited[dt] = true
		defer delete(visited, dt)

		if dt.IsList() || dt.IsMap() {
			if dt.MaxElements <= 0 {
				if lim, hasPlat := cost.PlatformLimits[path]; !hasPlat || !lim.Bounded {
					assert.Truef(t, intentionallyUnbounded[path],
						"unbounded platform collection %q in %s CEL context: add a bound in internal/limits/limits.go + internal/validation/decltype/reflect.go + internal/validation/cost/size_model.go, or explicitly allowlist it",
						path, ctxName)
				}
			}
			if dt.ElemType != nil {
				checkNode(t, ctxName, path+".@values", dt.ElemType, visited)
			}
			return
		}

		for fieldName, f := range dt.Fields {
			if f == nil || f.Type == nil {
				continue
			}
			checkNode(t, ctxName, path+"."+fieldName, f.Type, visited)
		}
	}

	for ctxName, roots := range contexts {
		t.Run(ctxName, func(t *testing.T) {
			for rootName, rootType := range roots {
				if rootName == "parameters" || rootName == "environmentConfigs" {
					continue
				}
				checkNode(t, ctxName, rootName, rootType, make(map[*apiservercel.DeclType]bool))
			}
		})
	}
}
