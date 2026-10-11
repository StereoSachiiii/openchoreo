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

// R2: Bisection to find exact minimal N that reaches ErrCostLimitExceeded
func TestR2MinimalNBisection(t *testing.T) {
	// Find minimal N between 10 and 68 for 3-level nest
	low := 10
	high := 68
	minN := high

	var minWallTime time.Duration
	var minHeapAlloc int64

	for low <= high {
		mid := (low + high) / 2
		envVars := make([]v1alpha1.EnvVar, mid)
		for i := 0; i < mid; i++ {
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
		_ = yaml.Unmarshal([]byte(ctYAML), &ct)

		comp := v1alpha1.Component{}
		workload := v1alpha1.Workload{
			Spec: v1alpha1.WorkloadSpec{
				WorkloadTemplateSpec: v1alpha1.WorkloadTemplateSpec{
					Container: v1alpha1.Container{Image: "alpine:latest", Env: envVars},
				},
			},
		}
		env := v1alpha1.Environment{Spec: v1alpha1.EnvironmentSpec{DataPlaneRef: &v1alpha1.DataPlaneRef{Name: "dp-1", Kind: "DataPlane"}}}
		dp := v1alpha1.DataPlane{Spec: v1alpha1.DataPlaneSpec{SecretStoreRef: &v1alpha1.SecretStoreRef{Name: "store-1"}}}

		input := &componentpipeline.RenderInput{
			ComponentType: &ct,
			Component:     &comp,
			Workload:      &workload,
			Environment:   &env,
			DataPlane:     &dp,
			Metadata: pipelinecontext.MetadataContext{
				Name: "test-r2", Namespace: "default", ComponentName: "comp", ComponentUID: "u1", ComponentNamespace: "default",
				ProjectName: "p1", ProjectUID: "up1", DataPlaneName: "dp-1", DataPlaneUID: "udp1",
				EnvironmentName: "dev", EnvironmentUID: "uenv1", Labels: map[string]string{}, Annotations: map[string]string{},
				PodSelectors: map[string]string{"app": "test"},
			},
		}

		runtime.GC()
		var m1, m2 runtime.MemStats
		runtime.ReadMemStats(&m1)
		p := componentpipeline.NewPipeline()
		start := time.Now()
		_, err := p.Render(context.Background(), input)
		wall := time.Since(start)
		runtime.ReadMemStats(&m2)
		heap := int64(m2.TotalAlloc - m1.TotalAlloc)

		if template.IsTerminalRenderError(err) {
			minN = mid
			minWallTime = wall
			minHeapAlloc = heap
			high = mid - 1
		} else {
			low = mid + 1
		}
	}

	t.Logf("R2 Bisected MINIMAL N: %d per level", minN)
	t.Logf("R2 Minimal Metrics: wall_time=%v | heap_alloc=%d bytes (%.2f MB)",
		minWallTime, minHeapAlloc, float64(minHeapAlloc)/(1024*1024))
	fmt.Printf("R2_MINIMAL_RAW: min_N=%d wall_time=%v heap_alloc_bytes=%d\n", minN, minWallTime, minHeapAlloc)
}
