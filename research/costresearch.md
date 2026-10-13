#  Kubernetes CRD Static Worst-Case CEL Cost Estimation (KEP-2876)
---

## 1. Background & The Need for Static Worst-Case Cost Estimation

### 1.1 The Vulnerability in Sandboxed Expression Engines
The Common Expression Language (CEL) is non-Turing complete and lacks arbitrary loops/side-effects. However, it permits comprehensions (`.map()`, `.filter()`, `.all()`, `.exists()`), nested traversals, string operations (e.g. `split()`, regex matching `find()`), and large collection constructions.

A single innocent-looking expression evaluated in-process by an API server or controller reconciler can exhaust CPU and memory:
```cel
// Example of DoS / Algorithmic Complexity Attack:
lists.range(10000).map(x, lists.range(10000).map(y, x + y))
```
- **Without Bounds**: Generates $10^8$ operations, freezing the goroutine and choking cluster throughput.
- **Render-Time Detection is Too Late**: If caught only at render/reconcile time, the bad resource is already stored in `etcd`, causing constant crashloops, slow reconciles, and blocked releases.

### 1.2 The KEP-2876 Solution
Kubernetes KEP-2876 introduced **two-tiered cost enforcement** for CRD validation (`x-kubernetes-validations`):
1. **Admission/Write Time (Static Worst-Case Cost Analysis)**:
   When a CRD is created or updated, the API server calculates the theoretical **worst-case upper bound** cost of every CEL expression using the OpenAPI v3 schema constraints (`maxItems`, `maxLength`, `maxProperties`).
   - If `StaticEstimatedCost > Limit`, the CRD is **rejected at write time**.
2. **Runtime (Actual Execution Metering)**:
   When an instance of the custom resource is created or modified, the actual execution cost is metered against a runtime budget (`PerCallLimit` = $1,000,000$, `RuntimeCELCostBudget` = $10,000,000$).

---

## 2. Kubernetes Cost Architecture & Source Code Breakdown

The core Kubernetes implementation is split across `k8s.io/apiextensions-apiserver` and `k8s.io/apiserver/pkg/cel`:

```
┌───────────────────────────────────────────────────────────────────────────────────┐
│                           Kubernetes CRD Pipeline                                 │
└─────────────────────────────────────────┬─────────────────────────────────────────┘
                                          │
                        1. OpenAPI v3 Structural Schema
                                          │
                                          ▼
                ┌───────────────────────────────────────────────────┐
                │          apiservercel.SchemaDeclType()            │
                │  - Converts Structural Schema -> DeclType Tree    │
                │  - Decorates DeclType with MinSerializedSize      │
                │  - Computes DeclType.MaxElements from bounds or:  │
                │     (MaxRequestSizeBytes / MinSerializedSize)     │
                └─────────────────────────┬─────────────────────────┘
                                          │
                                          ▼
                ┌───────────────────────────────────────────────────┐
                │      cel.Compile() & sizeEstimator.EstimateSize() │
                │  - AST analysis by cel-go CostEstimator           │
                │  - Maps element.Path() -> DeclType node           │
                │  - Returns SizeEstimate{Min, Max: MaxElements}    │
                └─────────────────────────┬─────────────────────────┘
                                          │
                                          ▼
                ┌───────────────────────────────────────────────────┐
                │             library.CostEstimator                 │
                │  - Static cost models for built-in/extended funcs │
                │  - Regex / split / join / format cost equations   │
                └─────────────────────────┬─────────────────────────┘
                                          │
                                          ▼
                ┌───────────────────────────────────────────────────┐
                │      checker.Cost(ast.NativeRep(), estimator)     │
                │  - Returns CostEstimate{Min, Max}                 │
                │  - Enforces StaticEstimatedCRDCostLimit (10^8)    │
                └───────────────────────────────────────────────────┘
```

### 2.1 The DeclType Tree & Automatic Sizing Calculations
Located in `k8s.io/apiserver/pkg/cel/common/schemas.go`:

1. **Max Request Size Benchmark**:
   Kubernetes defines `MaxRequestSizeBytes = 3 * 1024 * 1024` (3 MB) as the upper physical limit for any JSON serialized payload received by `kube-apiserver`.
2. **List / Array Maximum Size**:
   - If `maxItems` is explicitly provided in the schema:
     $$\text{MaxElements} = \text{maxItems}$$
   - If `maxItems` is **omitted** (unbounded list):
     Kubernetes does not assume $\infty$; it calculates the physical maximum number of items of minimum size that could fit in a 3 MB request:
     $$\text{MaxElements} = \frac{\text{MaxRequestSizeBytes} - 2}{\text{item.MinSerializedSize} + 1}$$
     *For a list of strings (`minSize = 2`), $\text{MaxElements} \approx \frac{3,145,728}{3} \approx 1,048,576$ elements!*
3. **Map / Object Properties Maximum Size**:
   - If `maxProperties` is explicitly provided:
     $$\text{MaxElements} = \text{maxProperties}$$
   - If `maxProperties` is omitted:
     $$\text{MaxElements} = \frac{\text{MaxRequestSizeBytes} - 2}{\text{property.MinSerializedSize} + 6}$$
4. **String Maximum Length**:
   - If `maxLength` is explicitly provided:
     $$\text{MaxElements} = \text{maxLength} \times 4 \quad \text{(to account for UTF-8 multi-byte runes)}$$
   - If `maxLength` is omitted:
     $$\text{MaxElements} = \text{MaxRequestSizeBytes} - 2 \approx 3,145,726 \text{ characters}$$

---

## 3. cel-go Static Cost Engine Mathematics

### 3.1 Function Cost Formulas (from `k8s.io/apiserver/pkg/cel/library/cost.go`)

| Operation / Function | cel-go / K8s Cost Formula | Computational Rationale |
| :--- | :--- | :--- |
| **Field Selection** (`a.b`) | $1$ unit | Constant O(1) dictionary / struct lookup |
| **String Traversal** (`startsWith`, `contains`, `lowerAscii`) | $\lceil \text{Length}(s) \times 0.1 \rceil$ | $0.1$ cost per byte processed (10 bytes = 1 cost unit) |
| **String Replacement** (`s.replace(old, new)`) | $\lceil \text{Length}(s) \times 0.2 \rceil$ | Traversal + result string buffer allocation |
| **String Split** (`s.split(sep)`) | $\lceil \text{Length}(s) \times 0.2 \rceil$ | Traversal + list element slice allocation |
| **String Join** (`list.join(sep)`) | $\lceil \sum \text{Length}(\text{elems}) \times 0.1 \rceil$ | Traversal + single string allocation |
| **Regular Expressions** (`s.find(regex)`, `matches`) | $\lceil (1 + \text{Length}(s)) \times 0.1 \rceil \times \lceil \text{Length}(regex) \times 0.25 \rceil$ | Multiplicative cost simulating state machine transitions |
| **List Comprehension** (`list.map(x, expr)`) | $\text{Size}(list) \times \text{Cost}(expr)$ | Iterates $N$ times evaluating the inner body |
| **Nested Comprehensions** | $\text{Size}(\text{outer}) \times \text{Size}(\text{inner}) \times \text{Cost}(\text{body})$ | Quadratic/polynomial cost scaling |

---

## 4. Key Differences: Kubernetes CRD Validation vs. OpenChoreo Templates

| Aspect | Kubernetes CRDs (KEP-2876) | OpenChoreo (ComponentTypes & Traits) |
| :--- | :--- | :--- |
| **Evaluation Output** | Always **Boolean** (`true` = valid, `false` = invalid) | **Any Type** (Maps, Lists, Strings, Objects for resource generation) |
| **Scope of Expression** | Single scoped validation rule on `self` | Multi-expression interpolation (`image:${parameters.tag}`) across entire JSON YAML documents |
| **Iteration Mechanism** | CEL comprehensions (`.map()`, `.all()`) within the expression | **Both** CEL comprehensions **AND** template-level `forEach` resource loops |
| **Custom Functions** | Kubernetes library (`isSorted`, `ip`, `cidr`, `quantity`, `semver`) | OpenChoreo library (`oc_hash`, `oc_generate_name`, `oc_dns_label`, `oc_merge`, `oc_omit`) |
| **Context Variables** | Only `self` and `oldSelf` | Rich multi-variable environment: `parameters`, `environmentConfigs`, `workload`, `metadata`, `trait`, `dataplane` |

---

## 5. Architectural Strategy for OpenChoreo Cost Validation

### 5.1 OpenChoreo Schema Bounds Sizing Strategy
Unlike `kube-apiserver` which defaults to 3MB request capacity (leading to $1,000,000$ elements for unbounded slices), OpenChoreo templates are authoring abstractions. 

We apply calibrated pessimistic defaults:
- **Unbounded List Default**: `DefaultMaxItems = 100` (or `MaxRequestSizeBytes / minSize` for strict parity).
- **Unbounded String Default**: `DefaultMaxLength = 1024` chars.
- **Unbounded Map Default**: `DefaultMaxProperties = 50` entries.

### 5.2 OpenChoreo Specific Construct Formulations

#### 1. `forEach` Loops
A resource template with:
```yaml
forEach: ${parameters.services}
var: svc
template:
  apiVersion: v1
  kind: Service
  spec:
    ports: ${svc.ports.map(p, {"port": p.port})}
```
**Static Cost Calculation**:
$$\text{Cost}(\text{Resource}) = \text{Cost}(\text{forEach.expr}) + \left( \text{MaxItems}(\text{parameters.services}) \times \sum_{e \in \text{Template}} \text{Cost}(e) \right)$$
*If `parameters.services` is unbounded ($100$) and `svc.ports` is unbounded ($100$), the nested cost evaluates to $100 \times 100 = 10,000$ units, triggering immediate warning/budget scrutiny unless bounded.*

#### 2. OpenChoreo Custom Functions (`oc_*`)
Static cost estimators matching runtime behavior:
- `oc_hash(str)`: $1 + \lceil \text{MaxLength}(str) \times 0.1 \rceil$
- `oc_generate_name(str | list)`: $1 + \text{Size}(input) + \lceil \text{TotalBytes}(input) \times 0.1 \rceil$
- `oc_dns_label(str | list)`: $1 + \text{Size}(input) + \lceil \text{TotalBytes}(input) \times 0.1 \rceil$
- `oc_merge(map1, map2)`: $1 + \text{MaxProps}(map1) + \text{MaxProps}(map2)$
- `oc_omit()`: $1$ (constant)

#### 3. Platform Injected Context Variables
Platform variables (`workload`, `metadata`, `trait`, `dataplane`) have no user schema. We furnish synthetic `DeclType` trees with standard platform bounds:
- `metadata.name`: MaxLength 253 (DNS subdomain max)
- `metadata.namespace`: MaxLength 63 (DNS label max)
- `workload.spec.containers`: MaxItems 32

---

## 6. Budget Thresholds & Error UX Recommendations

### 6.1 Recommended Budgets & Findings
- **Runtime Budget Calibration**: The runtime per-expression limit and cumulative budget per render is **2,000,000** (see `internal/template/engine_cache.go`).
- Static estimates must certify $B_{\text{static}} \le B_{\text{runtime}}$ ($2,000,000$). Arbitrary lower thresholds like $10,000$ reject valid real-world templates (where legitimate single expressions have measured costs up to 36,141 units).
- **Per-ComponentType / Spec Limit**: Full render composite limit checked against $2,000,000$.

### 6.2 Actionable Author-Facing Error Message Design
```
Error from server (Forbidden): error when creating "ComponentType/web-service":
admission webhook "vcomponenttype-v1alpha1.kb.io" denied the request:
ComponentType.openchoreo.dev "web-service" is invalid:
  spec.resources[1].template.spec.routes: Invalid value:
    Expression: "${parameters.routes.map(r, r.backends.map(b, b.port))}"
    Estimated Cost: 2,125,200 (exceeds budget of 2,000,000)
    Cause: 'parameters.routes' is unbounded (calculated upper bound: 1,048,575 items), and nested 'r.backends' is unbounded.
    Resolution: Add 'maxItems' constraint to 'parameters.routes' (e.g. maxItems: 10) and 'r.backends' in your openAPIV3Schema.
```

---

## 7. References & Grounding Sources

1. **Kubernetes Enhancement Proposal (KEP-2876)**: *CRD Validation Expression Language*, https://github.com/kubernetes/enhancements/tree/master/keps/sig-api-machinery/2876-crd-validation-expression-language
2. **Kubernetes API Server CEL Cost Library Source**: `k8s.io/apiserver/pkg/cel/library/cost.go`
3. **Kubernetes Common SchemaDeclType & Sizer Source**: `k8s.io/apiserver/pkg/cel/common/schemas.go`
4. **Kubernetes CEL CRD Compilation & Validation Engine**: `k8s.io/apiextensions-apiserver/pkg/apiserver/schema/cel/compilation.go`
5. **Google CEL-Go Cost Checker API**: `github.com/google/cel-go/checker/cost.go`
6. **Kubernetes Official Docs**: *Validation Rules in CustomResourceDefinitions*, https://kubernetes.io/docs/tasks/extend-kubernetes/custom-resources/custom-resource-definitions/#validation-rules

---

## 8. Multi-Tier Architecture Analysis & Findings

When evaluating a multi-tier cost enforcement architecture across the OpenChoreo lifecycle (Authoring $\to$ Release $\to$ Runtime Reconciliation), the following findings and boundaries govern the design:

### Tier 1: Authoring Admission (ComponentType / Trait Webhooks)
- **Mechanism**: Tokenize and compile CEL expressions into AST; run static `cel.CostEstimator` over `DeclType` trees.
- **Why no Go collections/slices are built**: Static cost estimation never evaluates expressions or allocates memory for data. It calculates upper-bound costs mathematically using structural schema metadata (`maxItems`, `maxLength`, `maxProperties`).
- **Critical Finding (Platform Variables)**: OpenAPI parameter schemas (`parameters.*`) only account for a fraction of expression complexity. Many expensive comprehensions iterate over platform-injected roots (`workload.*`, `metadata.*`, `derived.*`). Tier 1 must construct synthetic `DeclType` trees with enforced platform bounds (e.g. `workload.endpoints`, DNS label length limits) to avoid false-positive UNKNOWN saturation or unmetered DoS.

### Tier 2: Release Admission (`ComponentRelease` Webhook)
- **Mechanism**: Validates concrete developer inputs (`parameters`) against the snapshotted OpenAPI schema (`maxItems`, `maxLength`) via JSON schema validators. Validates composite static cost for `(ComponentType, AttachedTraits[])`.
- **Anti-Pattern Avoided (No Pre-Evaluation in Webhooks)**: Webhooks must **not** attempt to render templates or build/traverse Go collections in memory to check sizes. Pre-evaluating in admission duplicates the controller rendering pipeline and exposes the kube-apiserver webhook to algorithmic DoS attacks. Real evaluation is deferred to the controller.
- **Scope**: `ReleaseBinding` admission is a no-op; the actual binding and schema snapshots are certified at `ComponentRelease` admission.

### Tier 3: Controller Reconciler (`ReleaseBindingReconciler`)
- **Mechanism**: In-memory LRU cache for compiled `cel.Program` ASTs, runtime `cel.CostObserver` (budget: 2,000,000 units), and bounded execution with `context.WithTimeout(ctx, timeout)`.
- **LRU Cache Role**: The cache only saves compilation CPU cycles; it does not protect against execution DoS. Safety is guaranteed solely by the runtime `CostObserver` and timeout context.
- **Poison Pill Prevention (Terminal Errors)**: If a render fails due to timeout or cost-budget exhaustion, controller-runtime must **not** blindly return `err` (which causes unbounded requeue loops and starves single-worker reconcile queues). Cost/timeout failures are deterministic; they must be classified as terminal (`IsTerminalRenderError`), set a `Ready=False` status condition, and terminate without requeuing until the spec changes.

---

## 9. Comprehensive Limits Reference (Kubernetes, JSON, Containers, & Runtime)

A unified reference consolidating physical network/etcd thresholds, Kubernetes admission sizing math, container field constraints, and sensible static/runtime defaults for OpenChoreo.

### 9.1 Physical Request & Storage Boundaries

| Boundary Layer | Metric / Parameter | Value | Downstream Behavior / Error |
| :--- | :--- | :--- | :--- |
| **API Server Request** | HTTP Body Limit (`MaxRequestSizeBytes`) | **3 MiB** (`3,145,728` bytes) | Rejected with HTTP `413 Request Entity Too Large` (`limit is 3145728`) |
| **etcd Raft Transaction** | Key-Value / Txn Limit (`--max-request-bytes`) | **1.5 MiB** (`1,572,864` bytes) | `etcdserver: request is too large` |
| **Single Manifest Storage** | `RenderedRelease.spec.resources` | **< 1.5 MiB** combined | Must fit within single etcd object transaction |
| **ConfigMap / Secret** | Total data size | **1 MiB** (`1,048,576` bytes) | Enforced by Kubernetes core API validation |

---

### 9.2 Kubernetes KEP-2876 Worst-Case Sizing Derivations (from `schemas.go`)

When an OpenAPI v3 structural schema omits explicit constraints (`maxItems`, `maxLength`, `maxProperties`), Kubernetes does not assume $\infty$; it computes the theoretical maximum that could fit in a single 3 MiB serialized JSON request:

$$\text{MaxRequestSizeBytes} = 3 \times 1024 \times 1024 = 3,145,728 \text{ bytes}$$

| Data Type | Sizing Formula | Derived Upper Bound | Rationale |
| :--- | :--- | :--- | :--- |
| **Unbounded String** | `MaxRequestSizeBytes - 2` | **3,145,726** characters | Full body minus enclosing `""` quotes |
| **Bounded String** | `maxLength * 4` | $4 \times \text{maxLength}$ bytes | UTF-8 multi-byte rune worst case (up to 4 bytes per rune) |
| **Unbounded List/Slice** | $\lfloor \frac{\text{MaxRequestSizeBytes} - 2}{\text{minSerializedItemSize} + 1} \rfloor$ | **1,048,575** items (strings)<br>**1,572,863** items (empty `{}`) | Subtract 2 for `[]`; min string `""` is 2 bytes + 1 comma |
| **Unbounded Map/Object** | $\lfloor \frac{\text{MaxRequestSizeBytes} - 2}{\text{minPropertySize} + 6} \rfloor$ | **393,215** entries | Subtract 2 for `{}`; 6 bytes for key `""` + `:` + comma |

---

### 9.3 Container, Pod, and Workload Limits

Container specs and workload definitions have practical structural ceilings enforced by API specs or runtime operational sanity:

| Field Path | Practical / Enforced Limit | Source / Standard | Sensible OpenChoreo Bound |
| :--- | :--- | :--- | :--- |
| **Container Image URI** | `1,024` chars | Docker / OCI Image Spec & DNS limits | `1,024` chars (POLICY) |
| **Container Command (`command`)**| `32` items | Practical exec argv slice | `32` items (POLICY) |
| **Container Arguments (`args`)** | `64` items | Practical exec argv slice | `64` items (POLICY) |
| **Environment Variables (`env`)** | `1,024` entries | `workload.container.env` (Linux exec env limits) | `1,024` items (ENFORCED in D4) |
| **Mounted Files / Configs** | `256` files | `workload.container.files` | `256` items (ENFORCED in D4) |
| **Endpoints (`endpoints`)** | `128` endpoints | `workload.endpoints` (Service ports) | `128` items (ENFORCED in D4) |
| **Endpoint Visibility** | `4` items | Enum values (`Cluster`, `Project`, etc.) | `4` items (MaxItems=4) |
| **Dependencies (Endpoints/Res)** | `50` items | `workload_types.go:215,224` | `50` items (ENFORCED in CRD) |
| **Env & File Bindings** | `50` items | `workload_types.go:242,250` | `50` items (ENFORCED in CRD) |
| **DNS Label (Namespace, Port)** | `63` chars | RFC 1123 label (`[a-z0-9]([-a-z0-9]*[a-z0-9])?`) | `63` chars (ENFORCED) |
| **DNS Subdomain (Resource Name)** | `253` chars | RFC 1123 subdomain | `253` chars (ENFORCED) |
| **Labels & Annotations** | `50` keys / values | Practical metadata ceiling | `50` entries (POLICY) |

---

### 9.4 Template Loop & Expansion Bounds

| Construct | Limit | Source | Behavior on Exceeded |
| :--- | :--- | :--- | :--- |
| **`forEach` loop items** | `100` per loop | `foreach.go:23` | Returns `ErrExpansionLimitExceeded` |
| **`forEach` render total** | `10,000` per render | `foreach.go:32` | Returns `ErrExpansionLimitExceeded` |
| **Max Schema Nesting** | `10` levels | `schema_sizer.go:23` | Halts recursion to prevent stack exhaustion |

---

### 9.5 CEL Static & Runtime Engine Limits

| Dimension | Static Threshold (Authoring) | Runtime Threshold (Reconciler) | Enforcement Mechanism |
| :--- | :--- | :--- | :--- |
| **Single CEL Expression** | $\le 2,000,000$ | **2,000,000** | Static: `checker.Cost()` over `DeclType`<br>Runtime: `cel.CostObserver` |
| **Total Render Cumulative** | $\le 2,000,000$ | **2,000,000** | Static: Template aggregator $\sum \text{Resource}$<br>Runtime: cumulative cost meter |
| **Evaluation Timeout** | N/A (compile-time) | **1s - 5s** (bounded) | `context.WithTimeout(ctx, timeout)` |
| **Render Failure Policy** | Admission Webhook rejection | Terminal error (`ctrl.Result{}, nil`) | `IsTerminalRenderError` (no requeue) |


