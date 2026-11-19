// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

// Package limits defines platform-wide compute, loop, and schema bounds.
package limits

import "sync/atomic"

// CEL Execution & Computational Budget Limits
const (
	// DefaultCELCostLimit is the computational ceiling for a single CEL expression.
	DefaultCELCostLimit uint64 = 1_000_000

	// RenderBudgetFactor scales the resolved per-expression cost limit into the cumulative render budget.
	RenderBudgetFactor uint64 = 5

	// MaxCumulativeRenderBudget is the total allowable compute across all expressions in a render pass.
	MaxCumulativeRenderBudget uint64 = DefaultCELCostLimit * RenderBudgetFactor

	// MaxListsRangeSize bounds lists.range so a single call cannot materialize an unbounded slice.
	MaxListsRangeSize int64 = 10_000
)

// ReconcileBudget returns the cumulative render budget given a per-expression cost limit.
// A zero limit resolves to DefaultCELCostLimit.
func ReconcileBudget(costLimit uint64) uint64 {
	if costLimit == 0 {
		costLimit = DefaultCELCostLimit
	}
	return costLimit * RenderBudgetFactor
}

// Loop & Fan-Out Expansion Limits
const (
	// MaxForEachItemsPerLoop is the maximum number of items any single forEach loop can expand to.
	// Calibrated empirically against live cluster contention to prevent single-loop CPU monopolization.
	MaxForEachItemsPerLoop uint64 = 250

	// MaxForEachItemsPerRender is the cumulative expansion cap across an entire render pass.
	// Calibrated to 2,000 to keep worst-case materialization (100 static templates + 2,000 loop items = 2,092 resources)
	// safely below the 3,000-resource latency cliff.
	MaxForEachItemsPerRender uint64 = 2_000

	// MaxPatchOperations bounds the total number of rendered JSON Patch operations applied across all traits.
	MaxPatchOperations uint64 = 2_000
)

// Platform Default OpenAPI Schema Bounds & Enforced CRD Context Constants
const (
	// DefaultSchemaMaxItems is the platform-enforced default maxItems for OpenAPI array schemas
	// when the author does not declare maxItems.
	DefaultSchemaMaxItems int64 = 1_000

	// DefaultSchemaMaxProperties is the platform-enforced default maxProperties for OpenAPI map/dictionary
	// schemas when the author does not declare maxProperties.
	DefaultSchemaMaxProperties int64 = 1_000

	// DefaultSchemaMaxLength is the platform-enforced default maxLength for OpenAPI string schemas
	// when the author does not declare maxLength or enum.
	DefaultSchemaMaxLength int64 = 4_096

	// DefaultMapKeyMaxLength is the maximum character length assumed for map keys (Kubernetes DNS subdomain length).
	DefaultMapKeyMaxLength int64 = 253

	// Enforced Kubernetes & OpenChoreo CRD Context Bounds (single source of truth for reflect.go, size_model.go, and cost estimators)
	MaxKubernetesNameLength    int64 = 253
	MaxDNSLabelLength          int64 = 63
	MaxMetadataPodSelectors    int64 = 8
	MaxContainerImageLength    int64 = 1_024
	MaxContainerCommands       int64 = 32
	MaxContainerArgs           int64 = 128
	MaxWorkloadEnvVars         int64 = 1_024
	MaxWorkloadFileVars        int64 = 256
	MaxConfigVolumeMounts      int64 = 512 // 256 config files + 256 secret files
	MaxWorkloadEndpoints       int64 = 128
	MaxEndpointVisibilities    int64 = 4
	MaxEndpointBackendRefs     int64 = 16
	MaxWorkloadDependencies    int64 = 50
	MaxEndpointResourcesPerEp  int64 = 256
	MaxContainerEnvFromEntries int64 = 2
	MaxEnvsByContainerEntries  int64 = 1
	MaxAppliedResources        int64 = 100
	MaxWorkflowExternalRefs    int64 = 100
)

// SchemaBounds holds the platform default bounds applied to OpenAPI v3 schemas when omitted by the author.
// A zero or negative field disables that default bound (falling back to physical payload ceiling estimation).
type SchemaBounds struct {
	MaxItems        int64
	MaxProperties   int64
	MaxLength       int64
	MaxMapKeyLength int64
}

// DefaultSchemaBounds returns the standard built-in platform schema bounds (disabled/0 by default,
// falling back to KEP-2876 physical payload ceiling estimation unless opted in).
func DefaultSchemaBounds() SchemaBounds {
	return SchemaBounds{
		MaxItems:        0,
		MaxProperties:   0,
		MaxLength:       0,
		MaxMapKeyLength: DefaultMapKeyMaxLength,
	}
}

// OptInSchemaBounds returns the recommended opt-in platform schema bounds (1000 / 1000 / 4096 / 253).
func OptInSchemaBounds() SchemaBounds {
	return SchemaBounds{
		MaxItems:        DefaultSchemaMaxItems,
		MaxProperties:   DefaultSchemaMaxProperties,
		MaxLength:       DefaultSchemaMaxLength,
		MaxMapKeyLength: DefaultMapKeyMaxLength,
	}
}

var activeSchemaBounds atomic.Pointer[SchemaBounds]

func init() {
	def := DefaultSchemaBounds()
	activeSchemaBounds.Store(&def)
}

// ActiveSchemaBounds returns the currently configured platform default schema bounds.
func ActiveSchemaBounds() SchemaBounds {
	if b := activeSchemaBounds.Load(); b != nil {
		return *b
	}
	return DefaultSchemaBounds()
}

// SetActiveSchemaBounds updates the process-wide platform default schema bounds.
func SetActiveSchemaBounds(b SchemaBounds) {
	if b.MaxMapKeyLength <= 0 {
		b.MaxMapKeyLength = DefaultMapKeyMaxLength
	}
	cp := b
	activeSchemaBounds.Store(&cp)
}
