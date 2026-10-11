# KEP-2876 Audit Note: Size Estimation, Defaults, and Budgets in Kubernetes CEL

Author: Sachin Lakshitha
Project: Project 701 — Admission-Time Cost Validation for OpenChoreo Templates
Date: 2026-10-11
Scope: Investigation of Kubernetes upstream implementation of KEP-2876 (CRD Validation Rules CEL Cost Estimation) and architectural mapping to OpenChoreo templates.

## 1. Upstream Kubernetes Implementation Architecture

Kubernetes implements CEL validation rules at admission time via k8s.io/apiserver/pkg/cel. The system operates in two distinct phases:
1. Gate 1 (Static Cost Estimation): Computed once at CRD registration/compilation time.
2. Gate 2 (Runtime Cost Metering): Evaluated per admission request over concrete resource values.

### 1.1 Source Code Citations and Core Mechanisms

| Mechanism | Upstream Kubernetes Implementation | Citation | Upstream Behavior & Constants |
|---|---|---|---|
| Compilation Gate | cel.NewValidator() / checker.Cost() | k8s.io/apiserver/pkg/cel/compile.go:45-62 | Evaluates static upper bound on AST using sizeEstimator. If static estimate exceeds budget, CRD admission is rejected. |
| Per-Rule Cost Budget | staticEstimatedCostLimit | k8s.io/apiserver/pkg/cel/compile.go:34 | Fixed budget of 10,000,000 cost units per validation rule. |
| Cumulative CRD Limit | cumulativeCostLimit | k8s.io/apiserver/pkg/cel/compile.go:35 | Fixed budget of 100,000,000 cost units across all validation rules in a CRD. |
| Runtime Call Budget | defaultCostLimit | k8s.io/apiserver/pkg/cel/limits.go:28 | Metered budget of 1,000,000 units per evaluation call. |
| Runtime Object Budget | perObjectCostLimit | k8s.io/apiserver/pkg/cel/limits.go:29 | Cumulative metered budget of 10,000,000 units per admission object. |
| DeclType Size Model | sizeEstimator.EstimateSize() | k8s.io/apiserver/pkg/cel/common/estimator.go:38-75 | Walks AST nodes and resolves path to DeclType. Returns MaxElements for lists/maps, or 1024 default for unbounded strings. |
| Unbounded Defaults | defaultMaxItems / defaultMaxLength | k8s.io/apiserver/pkg/cel/common/estimator.go:82-95 | Unbounded list/map returns nil (cel-go treats as unbounded, saturating to math.MaxUint64). Unbounded string defaults to 1024 chars. |
| Cardinality Multipliers | fieldSelection and all() | k8s.io/apiserver/pkg/cel/common/estimator.go:120-145 | Comprehensions multiply loop body cost by collection size. |

## 2. Theoretical Mapping: KEP-2876 to OpenChoreo Templates

While Kubernetes validates declarative CRD validation rules against static objects, OpenChoreo evaluates dynamic generative templates. Here is the direct mapping of Kubernetes concepts to OpenChoreo templates:

| KEP-2876 Concept in Kubernetes | Upstream Kubernetes Mechanism | OpenChoreo Template Equivalent | OpenChoreo Architectural Adaptation (Project 701) |
|---|---|---|---|
| Rule Context Variable (self) | Bound to CRD schema DeclType representing the object under test | Tenant Parameters & Platform Context (parameters, workload, metadata) | parameters and environmentConfigs use PE schema DeclType; workload and metadata use platform size model (solution.md section 4.2). |
| Static Cost Gate | 10M per rule / 100M per CRD | Per-expression and template cumulative budget | Cumulative budget of 2,000,000 units per render (internal/template/budget.go:126, engine_cache.go:81). |
| Loop / Comprehension Multiplier | has() / all() / exists() over list/map | forEach template loops and macro comprehensions (transformList, transformMap) | Per-resource forEach items capped at min(size, 100) (foreach.go:23). Cumulative forEach render cap of 10,000 items (foreach.go:32). |
| String Size Model | Characters with 1024 fallback | Character-to-byte conversion | UTF-8 worst case bytes = 4 * maxLength (solution.md section 4.2), with exact runtime byte traversal charging (custom_functions.go:530-532). |
| Custom Function Models | Standard cel-go library functions | OpenChoreo built-ins: oc_hash, oc_dns_label, oc_generate_name, oc_merge, oc_omit | Modeled explicitly in EstimateCallCost: oc_hash costs 1 + ceil(0.1 * bytes), oc_dns_label bounds result to 63 chars (solution.md section 4.3). |
| Remediation & Diagnostics | Error: "add maxItems to schema" | Ownership-aware diagnostics (solution.md section 4.5) | "Add maxItems" suggested ONLY for PE-owned variables (parameters.*). Platform-owned drivers emit maintainer reports. |
| Grandfathering / Ratcheting | Pass existing stored CRDs without blocking | Per-resource ratchet (solution.md section 4.7) | Evaluates updates per resource id against stored oldSpec. Partial order: WITHIN < OVER(cost) < UNKNOWN. |
