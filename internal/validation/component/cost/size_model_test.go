// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package cost_test

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	k8syaml "sigs.k8s.io/yaml"

	"github.com/openchoreo/openchoreo/api/v1alpha1"
	"github.com/openchoreo/openchoreo/internal/validation/component"
	"github.com/openchoreo/openchoreo/internal/validation/component/cost"
	"github.com/openchoreo/openchoreo/internal/validation/schemautil"
)

func TestSizeModel_TruthTable(t *testing.T) {
	known10 := cost.SizeBound{Max: 10, State: cost.StateKnown, Origin: cost.OriginEnforced}
	known20 := cost.SizeBound{Max: 20, State: cost.StateKnown, Origin: cost.OriginEnforced}
	knownZeroDeclared := cost.SizeBound{Max: 0, State: cost.StateKnown, Origin: cost.OriginDeclared}
	knownZeroDefault := cost.SizeBound{Max: 0, State: cost.StateKnown, Origin: cost.OriginDefault}
	unknown := cost.SizeBound{Max: math.MaxUint64, State: cost.StateUnknown, Origin: cost.OriginDefault}

	t.Run("Addition: KNOWN + KNOWN = KNOWN", func(t *testing.T) {
		res := cost.AddBounds(known10, known20)
		assert.Equal(t, cost.StateKnown, res.State)
		assert.Equal(t, uint64(30), res.Max)
	})

	t.Run("Addition: KNOWN + UNKNOWN = UNKNOWN", func(t *testing.T) {
		res := cost.AddBounds(known10, unknown)
		assert.Equal(t, cost.StateUnknown, res.State)
	})

	t.Run("Multiplication: KNOWN * KNOWN = KNOWN", func(t *testing.T) {
		res := cost.MultiplyBounds(known10, known20)
		assert.Equal(t, cost.StateKnown, res.State)
		assert.Equal(t, uint64(200), res.Max)
	})

	t.Run("Multiplication: KNOWN(0, DECLARED) * UNKNOWN = KNOWN(0)", func(t *testing.T) {
		res := cost.MultiplyBounds(knownZeroDeclared, unknown)
		assert.Equal(t, cost.StateKnown, res.State)
		assert.Equal(t, uint64(0), res.Max)
	})

	t.Run("Multiplication: KNOWN(0, DEFAULT) * UNKNOWN = UNKNOWN", func(t *testing.T) {
		res := cost.MultiplyBounds(knownZeroDefault, unknown)
		assert.Equal(t, cost.StateUnknown, res.State)
	})

	t.Run("Multiplication: KNOWN(>0) * UNKNOWN = UNKNOWN", func(t *testing.T) {
		res := cost.MultiplyBounds(known10, unknown)
		assert.Equal(t, cost.StateUnknown, res.State)
	})

	t.Run("Clamping: values >= 2^63 become UNKNOWN", func(t *testing.T) {
		huge := cost.SizeBound{Max: 1 << 62, State: cost.StateKnown, Origin: cost.OriginEnforced}
		res := cost.AddBounds(huge, huge) // 2^63
		assert.Equal(t, cost.StateUnknown, res.State)
	})
}

func TestPlatformBounds_SyncWithWorkloadCRD(t *testing.T) {
	// 1. workload.container.env bound = 1024
	assert.Equal(t, uint64(1024), cost.PlatformPolicyBounds["workload.container.env"].Max)
	assert.Equal(t, cost.OriginEnforced, cost.PlatformPolicyBounds["workload.container.env"].Origin)

	// 2. workload.container.files bound = 256
	assert.Equal(t, uint64(256), cost.PlatformPolicyBounds["workload.container.files"].Max)
	assert.Equal(t, cost.OriginEnforced, cost.PlatformPolicyBounds["workload.container.files"].Origin)

	// 3. workload.endpoints bound = 128
	assert.Equal(t, uint64(128), cost.PlatformPolicyBounds["workload.endpoints"].Max)
	assert.Equal(t, cost.OriginEnforced, cost.PlatformPolicyBounds["workload.endpoints"].Origin)

	// 4. workload.endpoints.@values.visibility bound = 4
	assert.Equal(t, uint64(4), cost.PlatformPolicyBounds["workload.endpoints.@values.visibility"].Max)
	assert.Equal(t, cost.OriginEnforced, cost.PlatformPolicyBounds["workload.endpoints.@values.visibility"].Origin)

	// 5. configurations.configs.envs & secrets.envs bounds = 1024
	assert.Equal(t, uint64(1024), cost.PlatformPolicyBounds["configurations.configs.envs"].Max)
	assert.Equal(t, uint64(1024), cost.PlatformPolicyBounds["configurations.secrets.envs"].Max)

	// 6. configurations.configs.files & secrets.files bounds = 256
	assert.Equal(t, uint64(256), cost.PlatformPolicyBounds["configurations.configs.files"].Max)
	assert.Equal(t, uint64(256), cost.PlatformPolicyBounds["configurations.secrets.files"].Max)
	assert.Equal(t, uint64(256), cost.PlatformPolicyBounds["configurations.toConfigFileList"].Max)
	assert.Equal(t, uint64(256), cost.PlatformPolicyBounds["configurations.toSecretFileList"].Max)

	// 7. Outer container groupings (single container invariant = 1)
	assert.Equal(t, uint64(1), cost.PlatformPolicyBounds["configurations.toConfigEnvsByContainer"].Max)
	assert.Equal(t, uint64(1), cost.PlatformPolicyBounds["configurations.toSecretEnvsByContainer"].Max)

	// 8. Verify reflect.go bounds match CRD limits and PlatformPolicyBounds
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

	aggregator := cost.NewTemplateAggregator(val)
	baseEnv := val.GetBaseEnv()

	// Negative Test: Quadratic loop over configurations.configs.envs (N=1024)
	// Calculated static cost is ~231M > 2,000,000 budget -> VerdictOverBudget
	exprEnv := `configurations.configs.envs.map(a, configurations.configs.envs.map(b, a.name + b.name)).size()`
	_, issues := baseEnv.Compile(exprEnv)
	if issues != nil && issues.Err() != nil {
		t.Logf("Compile error for exprEnv: %v", issues.Err())
	}
	estEnv := aggregator.EstimateExpression(exprEnv, baseEnv, "resources[test].template.data")
	t.Logf("estEnv: cost=%d, state=%s, verdict=%s", estEnv.Cost, estEnv.State, estEnv.Verdict)
	assert.Equal(t, cost.VerdictOverBudget, estEnv.Verdict, "quadratic over 1024 env vars must be rejected as OVER budget")
	assert.Greater(t, estEnv.Cost, cost.MaxCumulativeRenderBudget)
}



func TestStaticCost_LinearFilterEndpointsAdmission(t *testing.T) {
	val, err := component.NewCELValidator(component.ComponentTypeResource, component.SchemaOptions{})
	assert.NoError(t, err)

	aggregator := cost.NewTemplateAggregator(val)
	baseEnv := val.GetBaseEnv()

	// Positive Test: Legitimate linear transformMap with filtering over workload.endpoints (N=128)
	// (This matches the HTTPRoute endpoint filtering pattern used in service.yaml)
	exprEndpoints := `workload.endpoints.transformMap(name, ep, "external" in ep.visibility, ep).size()`
	_, issues2 := baseEnv.Compile(exprEndpoints)
	if issues2 != nil && issues2.Err() != nil {
		t.Logf("Compile error for exprEndpoints: %v", issues2.Err())
	}
	estEndpoints := aggregator.EstimateExpression(exprEndpoints, baseEnv, "resources[test].template.data")
	t.Logf("estEndpoints: cost=%d, state=%s, verdict=%s", estEndpoints.Cost, estEndpoints.State, estEndpoints.Verdict)
	assert.Equal(t, cost.VerdictWithinBudget, estEndpoints.Verdict, "linear filter over 128 endpoints must be admitted as WITHIN budget")
	assert.LessOrEqual(t, estEndpoints.Cost, cost.MaxCumulativeRenderBudget)
}

func TestServiceComponentType_StaticCostWithinBudget(t *testing.T) {
	serviceYAMLPath := "../../../../samples/getting-started/component-types/service.yaml"
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

	val, err := component.NewCELValidator(component.ComponentTypeResource, component.SchemaOptions{
		ParametersSchema:         paramSchema,
		EnvironmentConfigsSchema: envConfigSchema,
	})
	assert.NoError(t, err)

	aggregator := cost.NewTemplateAggregator(val)
	est, err := aggregator.EstimateComponentType(&ct)
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
	checkerWarn := cost.NewWebhookCostChecker(cost.ModeWarn)
	warnings, err := checkerWarn.ValidateComponentTypeCreate(context.Background(), &ct, paramSchema, envConfigSchema)
	assert.NoError(t, err, "service.yaml must be admitted under default warn mode")
	assert.Empty(t, warnings, "service.yaml produces zero ratchet/UNKNOWN warnings under warn mode")

	// Validate with WebhookCostChecker in enforce mode - must admit cleanly!
	checkerEnforce := cost.NewWebhookCostChecker(cost.ModeEnforce)
	warningsEnforce, errEnforce := checkerEnforce.ValidateComponentTypeCreate(context.Background(), &ct, paramSchema, envConfigSchema)
	assert.NoError(t, errEnforce, "service.yaml must be admitted under enforce mode")
	assert.Empty(t, warningsEnforce)
}

func TestStaticCost_GatewayHostnamesWithinBudget(t *testing.T) {
	val, err := component.NewCELValidator(component.ComponentTypeResource, component.SchemaOptions{})
	assert.NoError(t, err)

	aggregator := cost.NewTemplateAggregator(val)
	baseEnv := val.GetBaseEnv()

	expr := `[gateway.ingress.external.?http, gateway.ingress.external.?https]
          .filter(g, g.hasValue()).map(g, g.value().host).distinct()
          .map(h, metadata.environmentName + "-" + metadata.componentNamespace + "." + h)`

	est := aggregator.EstimateExpression(expr, baseEnv, "resources[test].template.spec.hostnames")
	assert.Equal(t, cost.StateKnown, est.State)
	assert.Equal(t, cost.VerdictWithinBudget, est.Verdict)
	assert.LessOrEqual(t, est.Cost, cost.MaxCumulativeRenderBudget)
}

func TestStaticCost_DistinctChainedComprehensionPreservesSize(t *testing.T) {
	val, err := component.NewCELValidator(component.ComponentTypeResource, component.SchemaOptions{})
	assert.NoError(t, err)

	aggregator := cost.NewTemplateAggregator(val)
	baseEnv := val.GetBaseEnv()

	// Regression guard: distinct() must propagate ResultSize so subsequent comprehensions are StateKnown.
	expr := `["a", "b"].distinct().map(h, h)`
	est := aggregator.EstimateExpression(expr, baseEnv, "resources[test].template.data")
	assert.Equal(t, cost.StateKnown, est.State, "comprehensions chained on distinct() must evaluate as StateKnown")
	assert.Equal(t, cost.VerdictWithinBudget, est.Verdict)
	assert.LessOrEqual(t, est.Cost, cost.MaxCumulativeRenderBudget)
}

func TestLoopVariableRenameInvariance(t *testing.T) {
	val, err := component.NewCELValidator(component.ComponentTypeResource, component.SchemaOptions{})
	assert.NoError(t, err)

	agg := cost.NewTemplateAggregator(val)

	// Original variable name: envConfig
	resOriginal := v1alpha1.ResourceTemplate{
		ID:      "env-config",
		ForEach: "${configurations.toConfigEnvsByContainer()}",
		Var:     "envConfig",
		Template: &runtime.RawExtension{
			Raw: []byte(`{"data": "${envConfig.envs.transformMapEntry(index, env, {env.name: env.value})}", "metadata": {"name": "${envConfig.resourceName}"}}`),
		},
	}
	estOriginal, err := agg.EstimateResource(resOriginal)
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
	estRenamed, err := agg.EstimateResource(resRenamed)
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

	agg := cost.NewTemplateAggregator(val)

	// Case 1: Method argument containing dots, e.g. foo.bar(nested.arg) -> shape rejected, falls back to UNKNOWN
	resWithDotsInArg := v1alpha1.ResourceTemplate{
		ID:      "dot-arg-loop",
		ForEach: "${configurations.toConfigEnvsByContainer(extra.arg.foo)}",
		Var:     "item",
		Template: &runtime.RawExtension{
			Raw: []byte(`{"metadata": {"name": "test"}}`),
		},
	}
	est1, _ := agg.EstimateResource(resWithDotsInArg)
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
	est2, _ := agg.EstimateResource(resNestedParens)
	assert.Equal(t, cost.StateUnknown, est2.State, "nested parens must fall back to UNKNOWN")
	assert.Equal(t, cost.MaxForEachItemsPerLoop, est2.LoopBound)
}

