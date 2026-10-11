// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package cost

import (
	"testing"

	"github.com/google/cel-go/cel"
	apiextschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
)

func TestCostEstimatorCalculations(t *testing.T) {
	// Create mock structural schema with maxItems = 5
	maxItemsFive := int64(5)
	schema := &apiextschema.Structural{
		Properties: map[string]apiextschema.Structural{
			"routes": {
				ValueValidation: &apiextschema.ValueValidation{
					MaxItems: &maxItemsFive,
				},
			},
		},
	}

	validator := NewValidator(schema, nil, 1000)

	env, err := cel.NewEnv(
		cel.Variable("parameters", cel.MapType(cel.StringType, cel.DynType)),
	)
	if err != nil {
		t.Fatalf("failed to create cel env: %v", err)
	}

	ast, issues := env.Compile("parameters.routes")
	if issues != nil && issues.Err() != nil {
		t.Fatalf("failed to compile: %v", issues.Err())
	}

	res, err := validator.ValidateCost(ast)
	if err != nil {
		t.Fatalf("validation failed: %v", err)
	}

	if res.Exceeded {
		t.Fatalf("expected bounded routes to pass budget, got cost %d", res.EstimatedCost)
	}
	t.Logf("Computed estimated cost: %d", res.EstimatedCost)
}
