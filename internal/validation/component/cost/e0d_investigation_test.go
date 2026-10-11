package cost_test

import (
	"fmt"
	"testing"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/checker"
	apiservercel "k8s.io/apiserver/pkg/cel"

	"github.com/openchoreo/openchoreo/internal/template"
	"github.com/openchoreo/openchoreo/internal/validation/component/cost"
)

func TestE0d_PerNodeBreakdownAndRepro(t *testing.T) {
	// Item 1 & 2: Detailed per-node cost breakdown for 33,294 expression
	expr := `workload.endpoints.transformList(name, ep, ("external" in ep.visibility && ep.type in ["HTTP", "GraphQL", "Websocket"]) ? [name] : []).flatten()`

	epType := apiservercel.NewObjectType("Endpoint", map[string]*apiservercel.DeclField{
		"type":       apiservercel.NewDeclField("type", apiservercel.StringType, false, nil, nil),
		"visibility": apiservercel.NewDeclField("visibility", apiservercel.NewListType(apiservercel.StringType, 1000), false, nil, nil),
	})
	endpointsType := apiservercel.NewMapType(apiservercel.StringType, epType, 32)
	workloadType := apiservercel.NewObjectType("Workload", map[string]*apiservercel.DeclField{
		"endpoints": apiservercel.NewDeclField("endpoints", endpointsType, false, nil, nil),
	})

	roots := map[string]*apiservercel.DeclType{
		"workload": workloadType,
	}
	coster := cost.NewDeclTypeCostEstimator(roots)

	opts := template.BaseCELExtensions()
	opts = append(opts, cel.Variable("workload", workloadType.CelType()))
	bEnv, err := cel.NewEnv(opts...)
	if err != nil {
		t.Fatalf("bEnv error: %v", err)
	}
	provider := apiservercel.NewDeclTypeProvider(workloadType, epType, endpointsType)
	provOpts, err := provider.EnvOptions(bEnv.CELTypeProvider())
	if err != nil {
		t.Fatalf("provider options error: %v", err)
	}
	env, err := bEnv.Extend(provOpts...)
	if err != nil {
		t.Fatalf("env error: %v", err)
	}

	compiled, issues := env.Compile(expr)
	if issues != nil && issues.Err() != nil {
		t.Fatalf("compile error: %v", issues.Err())
	}

	est, err := checker.Cost(compiled.NativeRep(), coster)
	if err != nil {
		t.Fatalf("cost error: %v", err)
	}
	t.Logf("OVERALL STATIC ESTIMATE: Min=%d, Max=%d", est.Min, est.Max)

	// Measure runtime at Websocket (last in whitelist) vs HTTP
	prog, err := env.Program(compiled, append([]cel.ProgramOption{
		cel.CostLimit(10_000_000),
	}, template.CustomFunctionCostOptions()...)...)
	if err != nil {
		t.Fatalf("program error: %v", err)
	}

	makeInput := func(proto string) map[string]any {
		m := make(map[string]any)
		for i := 0; i < 32; i++ {
			vis := make([]any, 0, 1000)
			for j := 0; j < 999; j++ {
				vis = append(vis, fmt.Sprintf("other_%d", j))
			}
			vis = append(vis, "external")
			m[fmt.Sprintf("ep%d", i)] = map[string]any{
				"type":       proto,
				"visibility": vis,
			}
		}
		return map[string]any{
			"workload": map[string]any{
				"endpoints": m,
			},
		}
	}

	_, detailsHTTP, err := prog.Eval(makeInput("HTTP"))
	if err != nil {
		t.Fatalf("eval HTTP error: %v", err)
	}
	costHTTP := *detailsHTTP.ActualCost()

	_, detailsWS, err := prog.Eval(makeInput("Websocket"))
	if err != nil {
		t.Fatalf("eval WS error: %v", err)
	}
	costWS := *detailsWS.ActualCost()

	t.Logf("RUNTIME ACTUAL COST @ HTTP (first in list):      %d", costHTTP)
	t.Logf("RUNTIME ACTUAL COST @ Websocket (last in list): %d", costWS)

	// Item 7: Gateway .? vs normal-select minimal repro
	t.Log("=== Item 7: Gateway .? vs normal-select minimal repro ===")
	// Expression A: with .?
	exprOpt := `[gateway.ingress.external.?http, gateway.ingress.external.?https].filter(g, g.hasValue()).map(g, g.value().host)`
	// Expression B: without .? (normal select)
	exprNorm := `[gateway.ingress.external.http, gateway.ingress.external.https].map(g, g.host)`

	httpIngressType := apiservercel.NewObjectType("HttpIngress", map[string]*apiservercel.DeclField{
		"host": apiservercel.NewDeclField("host", apiservercel.StringType, false, nil, nil),
	})
	extIngressType := apiservercel.NewObjectType("ExternalIngress", map[string]*apiservercel.DeclField{
		"http":  apiservercel.NewDeclField("http", httpIngressType, false, nil, nil),
		"https": apiservercel.NewDeclField("https", httpIngressType, false, nil, nil),
	})
	ingressType := apiservercel.NewObjectType("Ingress", map[string]*apiservercel.DeclField{
		"external": apiservercel.NewDeclField("external", extIngressType, false, nil, nil),
	})
	gwType := apiservercel.NewObjectType("Gateway", map[string]*apiservercel.DeclField{
		"ingress": apiservercel.NewDeclField("ingress", ingressType, false, nil, nil),
	})

	gwRoots := map[string]*apiservercel.DeclType{
		"gateway": gwType,
	}
	gwCoster := cost.NewDeclTypeCostEstimator(gwRoots)

	gwOpts := template.BaseCELExtensions()
	gwOpts = append(gwOpts, cel.Variable("gateway", gwType.CelType()))
	gwBase, err := cel.NewEnv(gwOpts...)
	if err != nil {
		t.Fatalf("gwBase error: %v", err)
	}
	gwProvider := apiservercel.NewDeclTypeProvider(gwType, ingressType, extIngressType, httpIngressType)
	gwProvOpts, err := gwProvider.EnvOptions(gwBase.CELTypeProvider())
	if err != nil {
		t.Fatalf("gwProvOpts error: %v", err)
	}
	gwEnv, err := gwBase.Extend(gwProvOpts...)
	if err != nil {
		t.Fatalf("gwEnv error: %v", err)
	}

	astOpt, _ := gwEnv.Compile(exprOpt)
	costOpt, _ := checker.Cost(astOpt.NativeRep(), gwCoster)
	t.Logf("GATEWAY WITH .? (OPTIONAL):    Min=%d Max=%d", costOpt.Min, costOpt.Max)

	astNorm, _ := gwEnv.Compile(exprNorm)
	costNorm, _ := checker.Cost(astNorm.NativeRep(), gwCoster)
	t.Logf("GATEWAY NORMAL SELECT:         Min=%d Max=%d", costNorm.Min, costNorm.Max)
}
