# Project 701: Admission-Time Cost Analysis for CEL Templates in OpenChoreo

## Metadata
* **Project ID & Title:** Project 701 — Admission-Time Cost Validation for CEL Templates in OpenChoreo
* **Mentor Name:** Chathuranga Dassanayake
* **Author / Contributor:** Sachin Lakshitha
* **Number of People:** 1
* **Project Duration:** 3 Months (12 Weeks)
* **Target Ecosystem & Stack:** Go, CEL (`cel-go`), Kubernetes CRDs, OpenAPI v3 Schemas, Controller-Runtime, Admission Webhooks

---

# 1. Problem and Evidence

OpenChoreo renders Platform Engineer (PE) templates (defined in `ComponentType`, `ClusterComponentType`, `Trait`, and `ClusterTrait`) with developer-supplied workload configurations and tenant parameters to produce Kubernetes manifests. Today, computational complexity is evaluated only during pipeline execution inside the controller-manager reconciler. Admission webhooks perform syntactic and type checking, but do not evaluate computational cost.

The investigation into the repository and corpus established the following empirical facts:

1. **Static Estimator Soundness Failure:**
   The current static cost estimator undercounts runtime execution cost. For the production expression:
   `workload.endpoints.transformList(name, ep, ("external" in ep.visibility && ep.type in ["HTTP", "GraphQL", "Websocket"]) ? [name] : []).flatten()`
   from `samples/component-with-configs.yaml`, the static estimate is 33,294 units while measured runtime cost is 33,336 units [VERIFIED research/findings.md:162-171]. The static estimator underestimates runtime execution by 42 cost units. This violates the primary soundness invariant $\text{Cost}_{\text{static}} \ge \text{Cost}_{\text{runtime}}$.

2. **Corpus Complexity Distribution & Missing Driver Ownership:**
   Analysis of all 703 expressions across 13 sample `ComponentType` resources in `samples/` demonstrates that 66 expressions exceed an arbitrary 10,000 unit threshold [VERIFIED research/findings.md:144-159]. All 66 expressions are driven entirely by platform-owned or developer-supplied context collections (`workload.*`, `gateway.*`, `derived.*`, `configurations.*`). Exactly 0 expressions above 10,000 units are driven by PE schema parameters (`parameters.*` or `environmentConfigs.*`). Admitting or rejecting templates based on unconstrained platform variables produces false rejections and blames PEs for fields they do not control.

3. **Unbounded Platform CRD Collections:**
   The `Workload` CRD (`api/v1alpha1/workload_types.go:60-80, 297-317`) defines `container.env` (slice of `EnvVar`), `container.files` (slice of `FileVar`), and `endpoints` (map of `WorkloadEndpoint`) with zero structural bounds: no `+kubebuilder:validation:MaxItems` and no `+kubebuilder:validation:MaxProperties` markers exist on these collections [VERIFIED api/v1alpha1/workload_types.go:75, 79, 306].

4. **Reconciler Resource Starvation from Deeply Nested Comprehensions:**
   A 3-level nested comprehension over container environment variables trips the runtime limit `defaultCELCostLimit = 2_000_000` ([VERIFIED internal/template/engine_cache.go:61](file:///c:/Users/sachin%20lakshitha/openchoreo/internal/template/engine_cache.go#L61)) at bisected input size $N = 46$ environment variables per level [VERIFIED research/findings.md:183-189]. A Workload resource with 46 environment variables requires under 2 KB of YAML, well within Kubernetes API server request size limits. Unbounded platform structures therefore permit templates to exhaust reconciler memory and CPU budgets under legitimate payloads.

5. **Reconciler Retry Loop on Deterministic Errors:**
   Existing runtime guards enforce bounds:
   - Per-expression limit: `defaultCELCostLimit = 2_000_000` ([VERIFIED internal/template/engine_cache.go:61](file:///c:/Users/sachin%20lakshitha/openchoreo/internal/template/engine_cache.go#L61))
   - Cumulative budget: `2_000_000` units with `renderBudgetFactor = 1` ([VERIFIED internal/template/engine_cache.go:81](file:///c:/Users/sachin%20lakshitha/openchoreo/internal/template/engine_cache.go#L81), [internal/template/budget.go:126](file:///c:/Users/sachin%20lakshitha/openchoreo/internal/template/budget.go#L126))
   - Fan-out caps: `maxForEachItems = 100` per loop ([VERIFIED internal/pipeline/component/renderer/foreach.go:23](file:///c:/Users/sachin%20lakshitha/openchoreo/internal/pipeline/component/renderer/foreach.go#L23)) and `maxForEachItemsPerRender = 10_000` per render ([VERIFIED internal/pipeline/component/renderer/foreach.go:32](file:///c:/Users/sachin%20lakshitha/openchoreo/internal/pipeline/component/renderer/foreach.go#L32)). Both return terminal errors wrapping `template.ErrExpansionLimitExceeded` ([VERIFIED internal/pipeline/component/renderer/foreach.go:67, 83](file:///c:/Users/sachin%20lakshitha/openchoreo/internal/pipeline/component/renderer/foreach.go#L67-L83)).
   However, the `ReleaseBinding` controller reconciler catches render errors and returns `ctrl.Result{}, fmt.Errorf("failed to render resources: %w", err)` ([VERIFIED internal/controller/releasebinding/controller.go:564-571](file:///c:/Users/sachin%20lakshitha/openchoreo/internal/controller/releasebinding/controller.go#L564-L571)). The controller does not consult `template.IsTerminalRenderError` ([VERIFIED internal/template/errors.go:40-44](file:///c:/Users/sachin%20lakshitha/openchoreo/internal/template/errors.go#L40-L44)), triggering infinite exponential backoff retries (5ms to 1000s) on deterministic failures.

---

# 2. Threat and Failure Model

### 2.1 Protected Assets
1. **Reconciler Availability:**
   The controller-manager reconciler operates with manager-wide concurrency `maxConcurrentReconciles: 1` ([VERIFIED helm/openchoreo-control-plane/values.yaml:772](file:///c:/Users/sachin%20lakshitha/openchoreo/helm/openchoreo-control-plane/values.yaml#L772)). A single template that consumes excessive CPU or trips repetitive retry loops starves the worker queue and blocks reconciliation for all tenant workloads. Project 701 protects reconciler CPU scheduling and memory budgets by certifying that admitted templates cannot exceed runtime limits under valid inputs.
2. **Author Feedback Loop:**
   Platform Engineers receive immediate, deterministic diagnostics at authoring time (`kubectl apply` or CLI lint) rather than discovering failures asynchronously in controller logs after a developer triggers a release.

### 2.2 Persona and Authoring Boundaries
OpenChoreo operates with distinct personas across the lifecycle:
- **Platform Engineers (PEs):** Author `ComponentType`, `ClusterComponentType`, `Trait`, and `ClusterTrait`. They define parameter schemas (`spec.parameters`, `spec.environmentConfigs`) and write CEL template expressions. PEs possess cluster-scoped or namespace-scoped administrative access.
- **Application Developers:** Author `Component` and `Workload` resources. They supply values for parameters and declare container requirements (env, files, endpoints).
- **Platform Maintainers:** Author core controllers, runtime built-in functions (`oc_*`), and platform context structures (`workload`, `metadata`, `gateway`, `derived`, `configurations`).

### 2.3 Explicit Non-Claims (No Security Framing)
This system is an **availability defense-in-depth and authoring guardrail**. It is **not** a secure multi-tenant cryptographic sandbox:
- PEs are trusted cluster actors; this project does not defend against malicious cluster administrators with arbitrary CRD write permissions.
- Project 701 does not guarantee wall-clock execution time bounds. As documented in runtime comments ([VERIFIED internal/template/custom_functions.go:26-37](file:///c:/Users/sachin%20lakshitha/openchoreo/internal/template/custom_functions.go#L26-L37)), CEL cost tracks AST memory and computational complexity, but native operations such as `strings.split()` over large inputs can consume seconds of wall-clock time before tripping cost limits.

---

# 3. Non-Goals

The following areas are explicitly out of scope for Project 701:

1. **Byte-Size and Blast-Radius Estimator:**
   Admission-time static modeling of serialized output byte sizes (e.g. 5 MB limits) is excluded. All rendered manifests are written to a single `RenderedRelease` custom resource (`spec.resources []RenderedManifest`) ([VERIFIED api/v1alpha1/renderedrelease_types.go:28](file:///c:/Users/sachin%20lakshitha/openchoreo/api/v1alpha1/renderedrelease_types.go#L28)). Serialized object size is constrained by the Kubernetes etcd limit (~1.5 MB) and the API server request size limit (3 MiB). Estimating YAML serialization bytes statically from CEL AST expressions is brittle and uncalibrated. Output size is enforced at runtime during manifest creation.
2. **Administrative Override Annotations in v1:**
   Bypass annotations (such as `openchoreo.dev/cel-cost-override: "true"`) are excluded. Bypassing admission checks allows unvalidated templates to degrade shared controller queues. Backward compatibility is handled by the mathematical cost ratchet and warn-only phased rollout.
3. **Ad-Hoc Unenforced Platform Bounds:**
   Hardcoded arbitrary bounds presented as factual system properties (such as assuming 1000 items without validation) are eliminated. Bounds must either be backed by CRD validation markers, enforced by runtime limiters, or explicitly labeled as platform policy defaults.
4. **Whole-Reconcile Aggregation at ComponentType Admission:**
   The cumulative render budget (2,000,000 units) spans the combination of a `ComponentType` and all attached `Trait` instances. Because traits are bound dynamically when a developer creates a `Component`, the complete composition is unknown at `ComponentType` admission time. Whole-reconcile budget checking is deferred to `ComponentRelease` admission.
