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

### 6.1 Recommended Budgets
1. **Per-Expression Limit ($B_{\text{expr}}$)**: `10,000` cost units.
2. **Per-Resource Template Limit ($B_{\text{res}}$)**: `50,000` cost units (accounts for `forEach` expansion).
3. **Per-ComponentType / Spec Limit ($B_{\text{total}}$)**: `200,000` cost units.

### 6.2 Actionable Author-Facing Error Message Design
```
Error from server (Forbidden): error when creating "ComponentType/web-service":
admission webhook "vcomponenttype-v1alpha1.kb.io" denied the request:
ComponentType.openchoreo.dev "web-service" is invalid:
  spec.resources[1].template.spec.routes: Invalid value:
    Expression: "${parameters.routes.map(r, r.backends.map(b, b.port))}"
    Estimated Cost: 25,200 (exceeds per-expression budget of 10,000)
    Cause: 'parameters.routes' is unbounded (assumed 100 items), and nested 'r.backends' is unbounded (assumed 100 items).
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
