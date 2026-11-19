// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package cost

import (
	apiextschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	"k8s.io/apimachinery/pkg/runtime"
)

// SchemaPair bundles the structural OpenAPI schemas extracted from a single template object.
type SchemaPair struct {
	Parameters         *apiextschema.Structural
	EnvironmentConfigs *apiextschema.Structural
}

// DomainCostEstimator defines the Open-Closed extension interface for domain-specific CEL cost estimation.
// Any extensible resource kind implements this interface without requiring changes to the admission gate.
type DomainCostEstimator interface {
	Kind() string
	Estimate(obj runtime.Object, schemas SchemaPair, opts ...Option) (TemplateEstimate, error)
}
