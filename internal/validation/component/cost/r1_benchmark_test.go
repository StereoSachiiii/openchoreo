package cost

import (
	"context"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/openchoreo/openchoreo/api/v1alpha1"
	pipelinecontext "github.com/openchoreo/openchoreo/internal/pipeline/component/context"
	componentpipeline "github.com/openchoreo/openchoreo/internal/pipeline/component"
	"sigs.k8s.io/yaml"
)

type testSnapshot struct {
	Spec struct {
		Component     v1alpha1.Component     `json:"component"`
		ComponentType v1alpha1.ComponentType `json:"componentType"`
		Workload      v1alpha1.Workload      `json:"workload"`
		Traits        []v1alpha1.Trait       `json:"traits,omitempty"`
	} `json:"spec"`
}

func BenchmarkR1PipelineRealRender(b *testing.B) {
	// Hypothesis: Wall time per pipeline render on real templates is < 500µs.
	// Falsifier: Wall time > 50ms per render.


	// Read docs from multi-doc yaml

	// Parse directly using testSnapshot pattern
	snapshotYAML := `
apiVersion: core.choreo.dev/v1alpha1
kind: ComponentEnvSnapshot
spec:
  environment: dev
  component:
    metadata:
      name: test-app
    spec:
      parameters:
        replicas: 2
  componentType:
    spec:
      parameters:
        openAPIV3Schema:
          type: object
          properties:
            replicas:
              type: integer
      resources:
        - id: deployment
          template:
            apiVersion: apps/v1
            kind: Deployment
            metadata:
              name: ${metadata.name}
            spec:
              replicas: ${parameters.replicas}
  workload: {}
`
	var snapshot testSnapshot
	if err := yaml.Unmarshal([]byte(snapshotYAML), &snapshot); err != nil {
		b.Fatalf("unmarshal snapshot: %v", err)
	}

	envYAML := `
apiVersion: openchoreo.dev/v1alpha1
kind: Environment
metadata:
  name: dev
  namespace: test-namespace
spec:
  dataPlaneRef:
    name: dev-dataplane
    kind: DataPlane
`
	var env v1alpha1.Environment
	if err := yaml.Unmarshal([]byte(envYAML), &env); err != nil {
		b.Fatalf("unmarshal env: %v", err)
	}

	dpYAML := `
apiVersion: openchoreo.dev/v1alpha1
kind: DataPlane
metadata:
  name: dev-dataplane
  namespace: test-namespace
spec:
  secretStoreRef:
    name: dev-vault-store
`
	var dp v1alpha1.DataPlane
	if err := yaml.Unmarshal([]byte(dpYAML), &dp); err != nil {
		b.Fatalf("unmarshal dp: %v", err)
	}

	input := &componentpipeline.RenderInput{
		ComponentType: &snapshot.Spec.ComponentType,
		Component:     &snapshot.Spec.Component,
		Workload:      &snapshot.Spec.Workload,
		Environment:   &env,
		DataPlane:     &dp,
		Metadata: pipelinecontext.MetadataContext{
			Name:               "test-app-dev-123",
			Namespace:          "test-namespace",
			ComponentName:      "test-app",
			ComponentUID:       "uid-comp-1",
			ComponentNamespace: "test-namespace",
			ProjectName:        "test-proj",
			ProjectUID:         "uid-proj-1",
			DataPlaneName:      "dev-dataplane",
			DataPlaneUID:       "uid-dp-1",
			EnvironmentName:    "dev",
			EnvironmentUID:     "uid-env-1",
			Labels:             map[string]string{"app": "test"},
			Annotations:        map[string]string{},
			PodSelectors:       map[string]string{"app": "test"},
		},
	}

	p := componentpipeline.NewPipeline()
	ctx := context.Background()

	// Verify once
	out, err := p.Render(ctx, input)
	if err != nil {
		b.Fatalf("render: %v", err)
	}
	if len(out.Resources) == 0 {
		b.Fatal("no resources rendered")
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := p.Render(ctx, input)
		if err != nil {
			b.Fatalf("render %d: %v", i, err)
		}
	}
}

func TestR1PipelineCostUnitDistribution(t *testing.T) {
	// Hypothesis: ns per cost unit on real pipeline render is between 50ns and 800ns.
	// Falsifier: ns per unit > 2,000ns.
	snapshotYAML := `
apiVersion: core.choreo.dev/v1alpha1
kind: ComponentEnvSnapshot
spec:
  environment: dev
  component:
    metadata:
      name: test-app
    spec:
      parameters:
        replicas: 2
  componentType:
    spec:
      parameters:
        openAPIV3Schema:
          type: object
          properties:
            replicas:
              type: integer
      resources:
        - id: deployment
          template:
            apiVersion: apps/v1
            kind: Deployment
            metadata:
              name: ${metadata.name}
            spec:
              replicas: ${parameters.replicas}
        - id: service
          template:
            apiVersion: v1
            kind: Service
            metadata:
              name: ${metadata.name + "-svc"}
            spec:
              ports:
                - port: 80
  workload: {}
`
	var snapshot testSnapshot
	if err := yaml.Unmarshal([]byte(snapshotYAML), &snapshot); err != nil {
		t.Fatalf("unmarshal snapshot: %v", err)
	}

	envYAML := `
apiVersion: openchoreo.dev/v1alpha1
kind: Environment
metadata:
  name: dev
  namespace: test-namespace
spec:
  dataPlaneRef:
    name: dev-dataplane
    kind: DataPlane
`
	var env v1alpha1.Environment
	_ = yaml.Unmarshal([]byte(envYAML), &env)

	dpYAML := `
apiVersion: openchoreo.dev/v1alpha1
kind: DataPlane
metadata:
  name: dev-dataplane
  namespace: test-namespace
spec:
  secretStoreRef:
    name: dev-vault-store
`
	var dp v1alpha1.DataPlane
	_ = yaml.Unmarshal([]byte(dpYAML), &dp)

	input := &componentpipeline.RenderInput{
		ComponentType: &snapshot.Spec.ComponentType,
		Component:     &snapshot.Spec.Component,
		Workload:      &snapshot.Spec.Workload,
		Environment:   &env,
		DataPlane:     &dp,
		Metadata: pipelinecontext.MetadataContext{
			Name:               "test-app-dev-123",
			Namespace:          "test-namespace",
			ComponentName:      "test-app",
			ComponentUID:       "uid-comp-1",
			ComponentNamespace: "test-namespace",
			ProjectName:        "test-proj",
			ProjectUID:         "uid-proj-1",
			DataPlaneName:      "dev-dataplane",
			DataPlaneUID:       "uid-dp-1",
			EnvironmentName:    "dev",
			EnvironmentUID:     "uid-env-1",
			Labels:             map[string]string{"app": "test"},
			Annotations:        map[string]string{},
			PodSelectors:       map[string]string{"app": "test"},
		},
	}

	p := componentpipeline.NewPipeline()
	ctx := context.Background()

	// Warm up
	for i := 0; i < 10; i++ {
		_, _ = p.Render(ctx, input)
	}

	const runs = 100
	durations := make([]time.Duration, runs)
	for i := 0; i < runs; i++ {
		start := time.Now()
		_, err := p.Render(ctx, input)
		if err != nil {
			t.Fatalf("render %d: %v", i, err)
		}
		durations[i] = time.Since(start)
	}

	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	p50 := durations[runs*50/100]
	p99 := durations[runs*99/100]

	// The pipeline evaluated 3 expressions: ${metadata.name} (cost 1), ${parameters.replicas} (cost 1), ${metadata.name + "-svc"} (cost 2)
	// Total CEL cost = 4 units.
	totalCostUnits := uint64(4)
	nsPerUnitP50 := float64(p50.Nanoseconds()) / float64(totalCostUnits)
	nsPerUnitP99 := float64(p99.Nanoseconds()) / float64(totalCostUnits)

	t.Logf("R1 Pipeline Real Render: p50 wall=%v | p99 wall=%v", p50, p99)
	t.Logf("R1 Per CEL Unit on Pipeline (CEL+Pipeline overhead): p50=%.2f ns/unit | p99=%.2f ns/unit", nsPerUnitP50, nsPerUnitP99)
	fmt.Printf("R1_RAW: p50_wall=%v p99_wall=%v p50_ns_unit=%.2f p99_ns_unit=%.2f\n", p50, p99, nsPerUnitP50, nsPerUnitP99)
}
