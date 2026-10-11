// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package cost

import (
	"math"
)

// SizeOrigin denotes the source of a size limit per Project 701 §4.2.
type SizeOrigin string

const (
	OriginDeclared SizeOrigin = "DECLARED" // From OpenAPI v3 schema markers (maxItems/maxLength/maxProperties)
	OriginEnforced SizeOrigin = "ENFORCED" // Enforced by Kubernetes or CRD schema marker
	OriginPolicy   SizeOrigin = "POLICY"   // Architectural convention / policy table
	OriginDefault  SizeOrigin = "DEFAULT"  // Implicit or unconstrained default (evaluates as UNKNOWN)
)

// SizeState represents knowledge certainty per Project 701 §4.2.
type SizeState string

const (
	StateKnown   SizeState = "KNOWN"
	StateUnknown SizeState = "UNKNOWN"
)

// SizeBound carries a bounded size with origin and knowledge state per Project 701 §4.2.
type SizeBound struct {
	Max    uint64
	State  SizeState
	Origin SizeOrigin
	Path   string
}

// PlatformPolicyBounds defines the authoritative platform collection size table per Project 701 §4.2.
var PlatformPolicyBounds = map[string]SizeBound{
	"metadata.name": {
		Max:    253,
		State:  StateKnown,
		Origin: OriginEnforced,
		Path:   "metadata.name",
	},
	"metadata.namespace": {
		Max:    63,
		State:  StateKnown,
		Origin: OriginEnforced,
		Path:   "metadata.namespace",
	},
	"metadata.componentName": {
		Max:    253,
		State:  StateKnown,
		Origin: OriginEnforced,
		Path:   "metadata.componentName",
	},
	"metadata.projectName": {
		Max:    63,
		State:  StateKnown,
		Origin: OriginEnforced,
		Path:   "metadata.projectName",
	},
	"metadata.environmentName": {
		Max:    63,
		State:  StateKnown,
		Origin: OriginEnforced,
		Path:   "metadata.environmentName",
	},
	"metadata.labels": {
		Max:    50,
		State:  StateUnknown, // POLICY origin evaluates UNKNOWN by default
		Origin: OriginPolicy,
		Path:   "metadata.labels",
	},
	"metadata.annotations": {
		Max:    50,
		State:  StateUnknown,
		Origin: OriginPolicy,
		Path:   "metadata.annotations",
	},
	"workload.dependencies.endpoints": {
		Max:    50,
		State:  StateKnown,
		Origin: OriginEnforced,
		Path:   "workload.dependencies.endpoints",
	},
	"workload.dependencies.resources": {
		Max:    50,
		State:  StateKnown,
		Origin: OriginEnforced,
		Path:   "workload.dependencies.resources",
	},
	"workload.container.image": {
		Max:    1024,
		State:  StateUnknown,
		Origin: OriginPolicy,
		Path:   "workload.container.image",
	},
	"workload.container.command": {
		Max:    32,
		State:  StateUnknown,
		Origin: OriginPolicy,
		Path:   "workload.container.command",
	},
	"workload.container.args": {
		Max:    64,
		State:  StateUnknown,
		Origin: OriginPolicy,
		Path:   "workload.container.args",
	},
	"workload.container.env": {
		Max:    1024,
		State:  StateUnknown, // DEFAULT origin evaluates UNKNOWN until D4 merges
		Origin: OriginDefault,
		Path:   "workload.container.env",
	},
	"workload.container.files": {
		Max:    256,
		State:  StateUnknown,
		Origin: OriginDefault,
		Path:   "workload.container.files",
	},
	"workload.endpoints": {
		Max:    128,
		State:  StateUnknown,
		Origin: OriginDefault,
		Path:   "workload.endpoints",
	},
	"workload.endpoints.@values.visibility": {
		Max:    4,
		State:  StateUnknown,
		Origin: OriginPolicy,
		Path:   "workload.endpoints.@values.visibility",
	},
	"gateway.ingress.external.http.host": {
		Max:    253,
		State:  StateUnknown,
		Origin: OriginPolicy,
		Path:   "gateway.ingress.external.http.host",
	},
	"gateway.ingress.external.https.host": {
		Max:    253,
		State:  StateUnknown,
		Origin: OriginPolicy,
		Path:   "gateway.ingress.external.https.host",
	},
	"gateway.ingress.internal.http.host": {
		Max:    253,
		State:  StateUnknown,
		Origin: OriginPolicy,
		Path:   "gateway.ingress.internal.http.host",
	},
	"gateway.ingress.internal.https.host": {
		Max:    253,
		State:  StateUnknown,
		Origin: OriginPolicy,
		Path:   "gateway.ingress.internal.https.host",
	},
}

// AddBounds applies saturating addition and knowledge propagation truth table per Project 701 §4.2.
func AddBounds(a, b SizeBound) SizeBound {
	if a.State == StateUnknown || b.State == StateUnknown {
		return SizeBound{Max: math.MaxUint64, State: StateUnknown}
	}
	sum := a.Max + b.Max
	if sum < a.Max || sum >= (1<<63) { // Saturation overflow or >= 2^63 reclassified as UNKNOWN
		return SizeBound{Max: math.MaxUint64, State: StateUnknown}
	}
	return SizeBound{Max: sum, State: StateKnown, Origin: a.Origin}
}

// MultiplyBounds applies saturating multiplication and knowledge propagation truth table per Project 701 §4.2.
func MultiplyBounds(a, b SizeBound) SizeBound {
	// Special row: KNOWN(0) with origin DECLARED or ENFORCED multiplied by UNKNOWN is KNOWN(0)
	if a.State == StateKnown && a.Max == 0 && (a.Origin == OriginDeclared || a.Origin == OriginEnforced) {
		return a
	}
	if b.State == StateKnown && b.Max == 0 && (b.Origin == OriginDeclared || b.Origin == OriginEnforced) {
		return b
	}

	if a.State == StateUnknown || b.State == StateUnknown {
		return SizeBound{Max: math.MaxUint64, State: StateUnknown}
	}

	// Overflow check
	if a.Max > 0 && b.Max > math.MaxUint64/a.Max {
		return SizeBound{Max: math.MaxUint64, State: StateUnknown}
	}
	prod := a.Max * b.Max
	if prod >= (1 << 63) {
		return SizeBound{Max: math.MaxUint64, State: StateUnknown}
	}
	return SizeBound{Max: prod, State: StateKnown, Origin: a.Origin}
}
