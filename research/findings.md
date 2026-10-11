# Findings: Verifying the Premises of the Cost-Validation Design

Answers to the third review, checked against this repo and the pinned modules
(`cel-go v0.30.0`, `k8s.io/apiserver v0.36.3`, `k8s.io/apiextensions-apiserver v0.36.3`).
Anything not verified is marked **UNVERIFIED**.

---

## Q1. Are developer parameters validated against the PE schema, including bounds?

**Yes, before any CEL runs. But only at release and render time, not when the Component is written.**

| Stage | Validates params against schema? | Evidence |
| :--- | :--- | :--- |
| `Component` admission | **No.** It only checks for unique trait instance names. | [component/webhook.go#L82-L86](file:///c:/Users/sachin%20lakshitha/openchoreo/internal/webhook/component/webhook.go#L82-L86) ("schema validation is handled by the controller") |
| `ReleaseBinding` admission | **No.** It is a no-op, and `environmentConfigs` are not checked. | [releasebinding/webhook.go#L110-L116](file:///c:/Users/sachin%20lakshitha/openchoreo/internal/webhook/releasebinding/webhook.go#L110-L116) |
| `ComponentRelease` admission | **Yes**, against the snapshotted schema | [componentrelease/webhook.go#L328](file:///c:/Users/sachin%20lakshitha/openchoreo/internal/webhook/componentrelease/webhook.go#L328) |
| Render (controller) | **Yes.** Defaults are applied, then values are validated before evaluation. | [context/component.go#L123-L133](file:///c:/Users/sachin%20lakshitha/openchoreo/internal/pipeline/component/context/component.go#L123-L133) |

Both validating paths call [`schema.ValidateWithJSONSchema`](file:///c:/Users/sachin%20lakshitha/openchoreo/internal/schema/definition.go#L166-L196), which wraps the Kubernetes `validation.NewSchemaValidator`. That is the standard OpenAPI validator, so it enforces `maxItems`, `maxLength` and `maxProperties`.

**Consequence:** the central premise holds. At render time, every `parameters.*` and `environmentConfigs.*` value is within its declared bounds. A static estimate at declared bounds is therefore a real upper bound for those roots.

**Gap:** a developer learns that their parameters are out of bounds only at release or render time, not when they apply the Component. That is a separate issue from this project, and I'd note it rather than fix it here.

---

## Q2. Do platform variables (`workload`, `metadata`, …) carry bounds?

**Partly in the CRD. Not at all in the CEL type environment.**

1. **The CEL environment ignores CRD markers.** Platform variables are typed by reflecting over the Go context structs ([decltype/reflect.go](file:///c:/Users/sachin%20lakshitha/openchoreo/internal/validation/component/decltype/reflect.go#L13-L59)), not from the CRD OpenAPI. Every slice and map gets a hardcoded `maxListSize = 1000` / `maxMapSize = 1000`. This is the undocumented policy limit you suspected, and it already exists in the codebase. Nothing at runtime enforces it.
2. **Platform strings appear to have `MaxElements = 0`.** Reflected strings use `apiservercel.StringType`, which is built with `NewSimpleTypeWithMinSize` and never sets `MaxElements`. An estimator that walks these DeclTypes would size `oc_hash(metadata.name)` at **zero**. That is an undercount, the one failure mode we must not have. *(Needs a one-line test to confirm.)*
3. **CRD bounds are sparse.** [workload_types.go](file:///c:/Users/sachin%20lakshitha/openchoreo/api/v1alpha1/workload_types.go) bounds `dependencies.endpoints` and `dependencies.resources` (`MaxItems=50`), the binding maps (`MaxProperties=50`), and the `source.*` strings. It does **not** bound `endpoints` (a map), `container.command`, `args`, `env`, `files`, or most strings.

**Proposal:** don't add markers to core CRDs as part of this project; that is a breaking API change and needs its own proposal. Instead:
- Keep **policy bounds** in the estimator, in one table: one entry per platform path, with the value and its source (`crd` or `policy`).
- Where the CRD already declares a bound, use it and tag it `crd`.
- Everywhere else, use a request-size-derived bound (see Q4) and tag it `policy`. Diagnostics say so explicitly, for example: "`workload.endpoints` is bounded by platform policy, not by your schema."
- Replace the hidden 1000/1000 constants with this table, so there is exactly one source of truth.

---

## Q4. What is the runtime budget, and can we state `B_static ≤ B_runtime`?

**The runtime limits already exist and were derived from measurements.** See [engine_cache.go#L43-L81](file:///c:/Users/sachin%20lakshitha/openchoreo/internal/template/engine_cache.go#L43-L81):

| Runtime quantity | Value | How it was derived (per the code comments) |
| :--- | :--- | :--- |
| Per-expression `CostLimit` | **2,000,000** | Memory arm: ¼ of 1 GiB ÷ 10 concurrent reconciles ÷ ~10 B per unit ≈ 2.68M, rounded down |
| Heaviest real single expression | **36,141** | Measured on samples with worst-case legitimate inputs |
| Per-reconcile budget | **2,000,000** (factor 1) | ≥10× over the heaviest whole render |
| Heaviest real whole render | **69,437** | Measured on the component pipeline |

**Consequences:**
- **The 10,000 per-expression budget in every previous draft is wrong.** It sits below a measured legitimate expression (36,141). A sound estimator (estimate ≥ actual) would therefore reject at least one real sample.
- **The local prototype already enforces that 10,000 gate on every webhook path.** See [cel_validator.go#L55](file:///c:/Users/sachin%20lakshitha/openchoreo/internal/validation/component/cel_validator.go#L55) and [L82-L89](file:///c:/Users/sachin%20lakshitha/openchoreo/internal/validation/component/cel_validator.go#L82-L89). It is uncommitted, but it must not ship as is.
- **The invariant the gate should certify:** *for every expression, the static estimate at declared bounds is ≤ the runtime per-expression limit (2,000,000).* In other words, nothing admitted can hit a runtime per-expression error with valid inputs. The budget is then derived from an existing, measured number instead of a new one, which resolves the "two conflicting limits" problem.
- **For comparison, Kubernetes (pinned v0.36.3) uses** `PerCallLimit = 1,000,000`, `RuntimeCELCostBudget = 10,000,000`, and a static per-expression limit `StaticEstimatedCostLimit = 10,000,000`. Its static limit equals the *total* runtime budget, not the per-call limit.
- **The per-reconcile budget can't be fully gated at ComponentType admission.** It covers the ComponentType plus every attached Trait, and that combination is only known when a Component is released. The ComponentType and Trait webhooks can gate per-expression cost. The whole-reconcile sum belongs at `ComponentRelease` admission, which already loads both snapshots.

**Defaults become coherent at this budget.** Q1 confirms parameters live inside a Component object, so the Kubernetes request-size derivation has a real physical basis here. The parameters DeclTypes already use it: [cel_env.go#L107](file:///c:/Users/sachin%20lakshitha/openchoreo/internal/validation/component/cel_env.go#L107) calls `model.SchemaDeclType`. Computed from the pinned sources:
- An unbounded string has `MaxElements = 3 MiB − 2 = 3,145,726` (`estimateMaxStringLengthPerRequest`). So `oc_hash` on it costs about 314,574, which **passes** a 2M budget.
- An unbounded list of strings has `(3 MiB − 2) / (2 + 1) = 1,048,575` items. Any comprehension over it costs ≥ 1M per unit of body cost, so a non-trivial body or any nesting **fails**.
- A declared `maxLength` is multiplied by 4 (`estimateMaxElementsFromMaxLength`), to convert runes to bytes. That handles your bytes-vs-characters concern for `parameters`, and the runtime `oc_*` estimators charge bytes.

This is your "huge defaults need huge budgets" point, and the codebase already has the matching budget.

---

## Q5. How does provenance resolve `r.backends` when `r` is an iteration variable?

**cel-go already does this.** In v0.30.0, [`pushIterValue` / `pushIterSingle`](file:///C:/Users/sachin%20lakshitha/go/pkg/mod/github.com/google/cel-go@v0.30.0/checker/cost.go) register comprehension variables with the path `rangePath + "@items"` (lists) or `"@keys"`/`"@values"` (maps). So `r.backends` arrives at `EstimateSize` as `["parameters","routes","@items","backends"]`. The Kubernetes CRD estimator ([compilation.go#L313-L345](file:///C:/Users/sachin%20lakshitha/go/pkg/mod/k8s.io/apiextensions-apiserver@v0.36.3/pkg/apiserver/schema/cel/compilation.go)) just walks the **DeclType** along that path and returns `MaxElements`.

**Decision: mirror the Kubernetes `sizeEstimator`. Don't re-walk Structural schemas.** The current prototype ([cost_estimator.go](file:///c:/Users/sachin%20lakshitha/openchoreo/internal/validation/component/cost/cost_estimator.go)) is wrong in four ways:
- It re-walks `apiextschema.Structural` with its own 100/1024/50 defaults. That disagrees with the DeclTypes the same environment was type-checked against.
- It ignores `@keys` and `@indices`.
- It handles only the `parameters` and `environmentConfigs` roots, so `workload.*`, `metadata.*` and **`forEach` variables** fall through to type-kind defaults. The DeclType for each `forEach` variable already exists in [foreach_types.go#L54](file:///c:/Users/sachin%20lakshitha/openchoreo/internal/validation/component/foreach_types.go#L54) (`VarDeclType`), so the fix is a root map `{varName → DeclType}`.
- It does not apply the ×4 rune adjustment.

**Remaining gaps (this is what still makes provenance risky):**
- **Ternaries, `dyn`, and function results have no path.** cel-go then uses `UnknownSizeEstimate()` = `[0, MaxUint64]`, so the estimate saturates. Trait patches type `resource` as `DynType` ([trait.go#L475](file:///c:/Users/sachin%20lakshitha/openchoreo/internal/validation/component/trait.go#L475)), and `oc_merge` returns `map<string, dyn>`. These are the most likely false positives. The corpus run must measure how often they happen.
- **Attribution is by sensitivity, not additive contribution, as you suggested.** For each unbounded path seen during estimation, re-estimate with that path clamped to a small bound, then rank paths by the cost reduction. Ceilings come from bisection, not closed form.

---

## Q6. How is rendered output stored?

**All rendered manifests go into a single `RenderedRelease` object**, as `spec.resources []RenderedManifest` ([renderedrelease_types.go#L28](file:///c:/Users/sachin%20lakshitha/openchoreo/api/v1alpha1/renderedrelease_types.go#L28)). So the binding blast-radius constraint is the per-object size of one CR. The 5 MB figure from the last draft could never be stored. **UNVERIFIED:** the effective ceiling depends on the cluster (apiserver request limit 3 MiB, etcd default ~1.5 MiB). The real ceiling is the smaller of the two.

A rendered-bytes estimator is therefore worth less than a simple resource-count bound plus a render-time size check. I'd keep bytes as a stretch goal.

---

## Q3. Measured ns per cost unit: **not done yet**

The code comments carry one calibration point: `lists.range(10000).map(...)` cost 150k and ran in 85 ms, about 570 ns per unit ([custom_functions.go#L26-L31](file:///c:/Users/sachin%20lakshitha/openchoreo/internal/template/custom_functions.go#L26-L31)). They also show the ratio is **non-linear** above about 10k list elements (10× the cost, 75× the time). With the invariant from Q4, the budget no longer *depends* on this number. It still needs a benchmark to confirm that 2M per expression stays inside the render timeout. I won't quote a latency until I've run that benchmark.

---

## Q7. Scope

I agree with the must/should/stretch split. Two adjustments:

- **Must:** a DeclType-walking estimator (mirroring K8s), the platform policy-bound table, sensitivity-based provenance, an end-to-end parity test (worst-case inputs at declared bounds, `CostTracking`, assert `estimate ≥ actual`), a coverage test (non-nil `EstimateCallCost` per **overload**), a corpus report with a negative corpus, and a warn-only webhook.
- **Should:** an enforcement flag, a per-unit ratchet (keyed by resource ID / expression path, old cost recomputed with the current estimator), a status condition or metric for over-budget templates, and the **whole-reconcile check at `ComponentRelease` admission** (moved up from implicit).
- **Stretch:** CLI, the bytes dimension, and the bypass annotation (if kept, bound to a hash of the acknowledged spec).
- **Disagreement:** none on content. I'd add "confirm the bundled Helm-chart ComponentTypes pass" as a Week-1 must, since it is cheap and blocks installs if it fails.

---

## What this changes in `solution.md`

| Section | Change |
| :--- | :--- |
| §1.2 defaults | Replace 100/1024/50 and the flat 1M with the K8s request-size derivation that is already in the parameters DeclTypes. Add the platform policy table. |
| §2 budgets | Replace 10,000 / 50,000 / 200,000 with the invariant `estimate ≤ runtime CostLimit (2,000,000)`. Move the whole-reconcile check to `ComponentRelease`. Drop the 5 MB and 50-resource figures. |
| §3 provenance | Mirror the K8s `sizeEstimator` over DeclTypes. Use sensitivity ranking. Document the `dyn` and unknown-size saturation. |
| §4 parity | End-to-end parity on fixtures, plus a per-overload coverage test. |
| §5 example | Generate it from a golden test. Delete the hand-written numbers. |
| §7 rollout | Warn-only for one release. Helm-chart templates must pass. |
| Prototype | Make the 10,000 gate in `cel_validator.go` warn-only, or remove it, before anything else. |

---

# Part II: Empirical Verification & Audit Trail (Investigation Closeout)

Verified evidence produced during the empirical investigation phase, establishing the technical ground truth for the Project 701 design specification.

## 1. Engine & Runtime Limits

- **[VERIFIED internal/template/engine_cache.go:61]** `defaultCELCostLimit = 2_000_000`. Memory arm derivation: ¼ of 1 GiB limit (268 MB) ÷ 10 concurrent reconciles ÷ ~10 B/unit ≈ 2.68M, rounded down to 2,000,000. transient allocation capped at 20 MB per evaluation.
- **[VERIFIED internal/template/engine_cache.go:62]** `interruptCheckFrequency = 100` (evaluates context cancellation every 100 iterations).
- **[VERIFIED internal/template/engine_cache.go:81]** `renderBudgetFactor = 1`. Cumulative budget per reconcile is `2_000_000 * 1 = 2_000_000` ([VERIFIED internal/template/budget.go:126](file:///c:/Users/sachin%20lakshitha/openchoreo/internal/template/budget.go#L126)).
- **[VERIFIED internal/pipeline/component/renderer/foreach.go:23]** `maxForEachItems = 100`. It returns `fmt.Errorf("%w: forEach expanded to %d items, exceeding the limit of %d", template.ErrExpansionLimitExceeded, n, maxForEachItems)` ([VERIFIED internal/pipeline/component/renderer/foreach.go:66-68](file:///c:/Users/sachin%20lakshitha/openchoreo/internal/pipeline/component/renderer/foreach.go#L66-68)). It errors and aborts; it does not truncate.
- **[VERIFIED internal/pipeline/component/renderer/foreach.go:32]** `maxForEachItemsPerRender = 10_000`. It returns `fmt.Errorf("%w: forEach expansions would exceed the cumulative limit of %d items in one render", template.ErrExpansionLimitExceeded, maxForEachItemsPerRender)` ([VERIFIED internal/pipeline/component/renderer/foreach.go:80-84](file:///c:/Users/sachin%20lakshitha/openchoreo/internal/pipeline/component/renderer/foreach.go#L80-84)). It errors; it does not truncate.
- **[VERIFIED internal/template/custom_functions.go:38]** `maxListsRangeSize = 10_000`. cel-go raises an untyped error before allocating.
- **[VERIFIED helm/openchoreo-control-plane/values.yaml:787]** `celCostLimit: 2000000`.
- **[VERIFIED helm/openchoreo-control-plane/values.yaml:794]** `renderTimeout: "0"` (disabled by default; main.go:311 default 0).
- **[VERIFIED helm/openchoreo-control-plane/values.yaml:772]** `maxConcurrentReconciles: 1`. Manager-wide setting applied at main.go:426. No per-controller override exists.
- **[VERIFIED internal/controller/releasebinding/controller.go:564-571]** ReleaseBinding Reconcile handles render errors via `MarkFalseCondition(ConditionReleaseSynced, ReasonRenderingFailed, msg)` and returns `ctrl.Result{}, fmt.Errorf("failed to render resources: %w", err)`. This triggers controller-runtime's default exponential rate-limiting retry (5ms to 1000s). `IsTerminalRenderError` is not called anywhere in controller logic; grep confirms it is only invoked in tests.

## 2. Corpus Expression Distribution & Ownership Analysis

Evaluation of all 703 expressions across the 13 sample ComponentTypes in `samples/`:
- **Cost ≤ 100**: 615 expressions (87.5%)
- **Cost 101 .. 1,000**: 22 expressions (3.1%)
- **Cost 1,001 .. 10,000**: 0 expressions (0.0%)
- **Cost 10,001 .. 100,000**: 25 expressions (3.6%) — values range from 32,200 to 33,294, driven by endpoint comprehensions over reflected default slice bounds (1,000).
- **Cost > 100k (Finite saturation artifacts)**: 2 expressions (0.3%) — values are 3,689,348,814,741,910,584 and 1,844,674,407,370,955,268 (saturation artifacts around 0.2 × 2^64 and 0.1 × 2^64 from unsigned arithmetic).
- **Cost = MaxUint64 (Saturated unknown)**: 39 expressions (5.5%).
- **Total above 10,000**: Exactly 66 expressions.
- **Ownership Breakdown of the 66 Expressions**:
  - 66 are driven entirely by platform-owned or developer-supplied runtime inputs (`workload.*`, `gateway.*`, `derived.*`, `configurations.*`).
  - Exactly 0 expressions above 10,000 are driven by Platform Engineer parameters (`parameters.*` or `environmentConfigs.*`).
  - Against the true runtime budget of 2,000,000, exactly 0 expressions in the corpus legitimately exceed the budget. All expressions exceeding 2M are in the UNKNOWN (saturated) bucket due to missing DeclType bounds on platform collections.

## 3. Disproof of Soundness on the Current Static Estimator (E0 Verdict: FAILED)

- **Expression under test**:
  `workload.endpoints.transformList(name, ep, ("external" in ep.visibility && ep.type in ["HTTP", "GraphQL", "Websocket"]) ? [name] : []).flatten()`
  (from sample `component-with-configs.yaml`).
- **Static Estimate**: 33,294 (with `ep.visibility` slice bound reflected as 1,000).
- **Measured Runtime Cost**: 33,336 (with 32 endpoints, 1,000 visibilities, `"external"` at index 999, `ep.type = "Websocket"`).
- **Delta**: $33,336 - 33,294 = +42$ units.
- **Verdict**: Soundness invariant $\text{Cost}_{\text{static}} \ge \text{Cost}_{\text{runtime}}$ is **VIOLATED** (33,294 < 33,336).
- **Root Cause**: The cel-go runtime charges +1 cost unit per created list element on `[name]` ($32 \times 1 = 32$) plus collection iteration setup overhead (+10 units) during runtime evaluation. The static AST estimator omitted these node allocations. Static estimators must never apply arbitrary additive safety margins; every node allocation cost in cel-go must be explicitly modeled in the AST size and cost rules.

## 4. The 36,141 Expression Provenance

- Cited in comment in `internal/template/engine_cache.go:45-47` as the costliest measured expression: a `transformMapEntry` comprehension over `envConfig.envs`.
- [INFERRED] Derived from $33 + 36N$ cost progression for map entry transformation at $N \approx 1,003$ entries.
- [UNVERIFIED] The exact benchmark script and data fixture used for this measurement were not committed to the repository.

## 5. Microbenchmark & Stress Profiling Measurements

- **R1 Benchmark (Pure CEL vs Pipeline)**:
  - Pure CEL `items.filter(...).map(...)` at $N=1,000$: 31,023 units, measured p50 latency = 5.97 ms (192.35 ns/unit), p99 latency = 22.98 ms (740.78 ns/unit).
  - Extrapolated to 2,000,000 budget: p50 ≈ 385 ms, p99 ≈ 1.48 s.
  - Baseline real pipeline render (Deployment + Service): ~88–118 µs/op, ~57 KB/op, 773 heap allocs.
- **R2 Stress Benchmark (Nested Comprehension Denial-of-Service)**:
  - A 3-level nested comprehension over container env vars:
    trips `ErrCostLimitExceeded` (2,000,000) at bisected $N=46$ environment variables per level (270 ms wall time, 58.5 MB heap allocation).
  - An API server / envtest accepts a Workload object with 46 environment variables without error (payload size < 2 KB). Unbounded platform context structures can therefore trigger denial-of-service in the reconciler unless bounded at the input schema.

