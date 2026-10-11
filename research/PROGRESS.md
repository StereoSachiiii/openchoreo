# Project 701: Admission-Time Cost Analysis - Progress & Verification Record

## 1. Task Status Overview
- **Phase:** Design Specification Committed (Transition from Investigation to Phase 0 Truth).
- **Core Design Artifacts in `research/`:**
  - `research/problem.md`: Problem statement, verified empirical evidence, threat & failure model, non-goals.
  - `research/solution.md`: Authoritative technical specification, component contracts (B1–B4, C1–C4, D1r, E1–E2), Mermaid diagrams A–F, differential soundness test plan (§6.1), rollout plan, compatibility, risks, 12-week schedule, and open items (§10).
  - `research/findings.md`: Empirical findings, Q1–Q7 reviews, and Part II audit trail.

---

## 2. Settled Empirical Ground Truth

### 2.1 Engine & Runtime Limits
- **Per-Expression CEL Cost Limit:** `defaultCELCostLimit = 2_000_000` ([VERIFIED internal/template/engine_cache.go:61](file:///c:/Users/sachin%20lakshitha/openchoreo/internal/template/engine_cache.go#L61)).
- **Per-Reconcile Cumulative Budget:** `renderBudgetFactor = 1`, resulting in `2_000_000` cumulative units ([VERIFIED internal/template/engine_cache.go:81](file:///c:/Users/sachin%20lakshitha/openchoreo/internal/template/engine_cache.go#L81), [internal/template/budget.go:126](file:///c:/Users/sachin%20lakshitha/openchoreo/internal/template/budget.go#L126)).
- **forEach Expansion Caps:**
  - Per-loop cap: `maxForEachItems = 100` ([VERIFIED internal/pipeline/component/renderer/foreach.go:23](file:///c:/Users/sachin%20lakshitha/openchoreo/internal/pipeline/component/renderer/foreach.go#L23)). It returns a terminal error wrapping `ErrExpansionLimitExceeded` ([VERIFIED internal/pipeline/component/renderer/foreach.go:66-68](file:///c:/Users/sachin%20lakshitha/openchoreo/internal/pipeline/component/renderer/foreach.go#L66-68)).
  - Per-render cumulative cap: `maxForEachItemsPerRender = 10_000` ([VERIFIED internal/pipeline/component/renderer/foreach.go:32](file:///c:/Users/sachin%20lakshitha/openchoreo/internal/pipeline/component/renderer/foreach.go#L32)). It returns a terminal error ([VERIFIED internal/pipeline/component/renderer/foreach.go:80-84](file:///c:/Users/sachin%20lakshitha/openchoreo/internal/pipeline/component/renderer/foreach.go#L80-84)).
- **lists.range Cap:** `maxListsRangeSize = 10_000` ([VERIFIED internal/template/custom_functions.go:38](file:///c:/Users/sachin%20lakshitha/openchoreo/internal/template/custom_functions.go#L38)).

### 2.2 Corpus Cost Distribution
- Across 703 expressions in `samples/`:
  - Cost $\le 100$: 615 expressions (87.5%)
  - Cost $101 \dots 1,000$: 22 expressions (3.1%)
  - Cost $1,001 \dots 10,000$: 0 expressions (0.0%)
  - Cost $10,001 \dots 100,000$: 25 expressions (3.6%)
  - Cost $> 100\text{k}$ (Finite saturation artifacts): 2 expressions (0.3%)
  - Cost = `MaxUint64` (Saturated unknown): 39 expressions (5.5%)
  - Total expressions $> 10,000$: Exactly 66 expressions. All 66 are driven by platform/developer variables (`workload.*`, `gateway.*`, `derived.*`, `configurations.*`), with 0 driven by PE parameters.
  - Saturated/unknown total: ~41 expressions (39 saturated + 2 finite overflow values).

### 2.3 Status of Estimator Soundness (Open Defect)
- Expression: `workload.endpoints.transformList(name, ep, ("external" in ep.visibility && ep.type in ["HTTP", "GraphQL", "Websocket"]) ? [name] : []).flatten()`
- Static estimate: 33,294 vs Measured runtime: 33,336.
- The 42-unit undercount ($33,336 - 33,294 = +42$) is an **open defect**, not a solved rule. Arbitrary margins or fitted constants are prohibited. It will be resolved by generating the differential table (§6.1, Task T1.4). Any node kind that cannot be fixed will be designated `UNKNOWN`.

### 2.4 Controller Behavior
- `ReleaseBinding` reconciler catches render errors and retries infinitely via exponential backoff ([VERIFIED internal/controller/releasebinding/controller.go:564-571](file:///c:/Users/sachin%20lakshitha/openchoreo/internal/controller/releasebinding/controller.go#L564-L571)). `template.IsTerminalRenderError` ([VERIFIED internal/template/errors.go:40-44](file:///c:/Users/sachin%20lakshitha/openchoreo/internal/template/errors.go#L40-L44)) is unused in production controllers. Project 701 D4b specifies halting retries on terminal render errors keyed by `inputsHash`.
