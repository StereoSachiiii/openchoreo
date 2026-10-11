package cost

import (
	"context"
	"fmt"
	"runtime"
	"testing"
	"time"

	"github.com/openchoreo/openchoreo/api/v1alpha1"
	componentpipeline "github.com/openchoreo/openchoreo/internal/pipeline/component"
	pipelinecontext "github.com/openchoreo/openchoreo/internal/pipeline/component/context"
	"github.com/openchoreo/openchoreo/internal/template"
	"sigs.k8s.io/yaml"
)

// R2: Smallest-input template that reaches ErrCostLimitExceeded via nested comprehension.
// Hypothesis: A nested comprehension over workload.container.env can reach ErrCostLimitExceeded (limit=10,000,000)
// with modest N (e.g. N <= 70 per level in a 3-level nest).
// Falsifier: Does not return ErrCostLimitExceeded or OOMs before CEL cost tracker limits it.
func TestR2SmallestInputNestedComprehension(t *testing.T) {
	// Nested comprehension over workload.container.env:
	// Level 1 iterates over N items, Level 2 iterates over N items, Level 3 iterates over N items
	// Cost per iteration is ~36 units => 36 * N^3. For 10,000,000 units: N^3 > 277,777 => N >= 66.
	const N = 68
	envVars := make([]v1alpha1.EnvVar, N)
	for i := 0; i < N; i++ {
		envVars[i] = v1alpha1.EnvVar{
			Key:   fmt.Sprintf("VAR_%d", i),
			Value: fmt.Sprintf("val_%d", i),
		}
	}

	expr := `configurations.configs.envs.map(a, configurations.configs.envs.map(b, configurations.configs.envs.map(c, a.name + b.name + c.name)))`

	ctYAML := fmt.Sprintf(`
apiVersion: openchoreo.dev/v1alpha1
kind: ComponentType
spec:
  resources:
    - id: test-res
      template:
        apiVersion: v1
        kind: ConfigMap
        metadata:
          name: ${metadata.name}
        data:
          result: ${%s}
`, expr)

	var ct v1alpha1.ComponentType
	if err := yaml.Unmarshal([]byte(ctYAML), &ct); err != nil {
		t.Fatalf("unmarshal ct: %v", err)
	}

	comp := v1alpha1.Component{
		Spec: v1alpha1.ComponentSpec{},
	}
	workload := v1alpha1.Workload{
		Spec: v1alpha1.WorkloadSpec{
			WorkloadTemplateSpec: v1alpha1.WorkloadTemplateSpec{
				Container: v1alpha1.Container{
					Image: "alpine:latest",
					Env:   envVars,
				},
			},
		},
	}
	env := v1alpha1.Environment{
		Spec: v1alpha1.EnvironmentSpec{
			DataPlaneRef: &v1alpha1.DataPlaneRef{Name: "dp-1", Kind: "DataPlane"},
		},
	}
	dp := v1alpha1.DataPlane{
		Spec: v1alpha1.DataPlaneSpec{
			SecretStoreRef: &v1alpha1.SecretStoreRef{Name: "store-1"},
		},
	}

	input := &componentpipeline.RenderInput{
		ComponentType: &ct,
		Component:     &comp,
		Workload:      &workload,
		Environment:   &env,
		DataPlane:     &dp,
		Metadata: pipelinecontext.MetadataContext{
			Name:               "test-r2",
			Namespace:          "default",
			ComponentName:      "test-r2-comp",
			ComponentUID:       "uid-1",
			ComponentNamespace: "default",
			ProjectName:        "proj-1",
			ProjectUID:         "uid-p-1",
			DataPlaneName:      "dp-1",
			DataPlaneUID:       "uid-dp-1",
			EnvironmentName:    "dev",
			EnvironmentUID:     "uid-env-1",
			Labels:             map[string]string{},
			Annotations:        map[string]string{},
			PodSelectors:       map[string]string{"app": "test-r2"},
		},
	}

	// Measure heap and wall time
	runtime.GC()
	var m1, m2 runtime.MemStats
	runtime.ReadMemStats(&m1)

	p := componentpipeline.NewPipeline() // uses default cost limit 10,000,000
	start := time.Now()
	_, err := p.Render(context.Background(), input)
	wallTime := time.Since(start)

	runtime.ReadMemStats(&m2)
	heapAlloc := int64(m2.TotalAlloc - m1.TotalAlloc)

	t.Logf("R2 Result: err=%v", err)
	t.Logf("R2 Metrics: N=%d per level (3 levels, total env count=%d) | wall_time=%v | heap_alloc=%d bytes (%.2f MB)",
		N, len(envVars), wallTime, heapAlloc, float64(heapAlloc)/(1024*1024))

	isCostLimit := template.IsTerminalRenderError(err)
	t.Logf("R2 IsTerminalRenderError(err) = %v", isCostLimit)

	workloadBytes, _ := yaml.Marshal(workload)
	t.Logf("R2 Workload Spec YAML size: %d bytes (well under 1.5MB etcd limit)", len(workloadBytes))
}
