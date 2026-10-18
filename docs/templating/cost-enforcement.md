# CEL Cost Enforcement

Templates are tenant-authored, and rendering runs in the controller. Without a bound, a
single expression such as

```cel
${size(lists.range(2000).map(x, lists.range(2000).map(y, x + y)))}
```

(4M inner iterations, roughly a second of CPU) runs per evaluation, per expression, per
reconcile. The guards below bound that work. They live in `internal/template`, the four
rendering pipelines under `internal/pipeline`, and `internal/controller`.

Three layers cover what the others cannot see, plus a wall-clock backstop:

| Protection Mechanism | Guarded Scope | Default Limit | Failure Behavior |
|---|---|---|---|
| `cel.CostLimit` | Single CEL expression | 2,000,000 units | Rejection (`ErrCostLimitExceeded`) |
| `CostBudget` | Total render budget across all resources | 2,000,000 units (1× limit) | Rejection (`ErrCostBudgetExceeded`) |
| Custom function estimator | OpenChoreo `oc_*` functions | Measured input traversal | Feeds per-expression and cumulative limits |
| `forEach` expansion caps | Per single loop / cumulative per render | 100 items / 10,000 items | Rejection (`ErrExpansionLimitExceeded`) |
| Patch operations | Per component render | 10,000 operations | Rejection (`ErrExpansionLimitExceeded`) |
| `lists.range` size | Elements generated per call | 10,000 elements | Plain evaluation error |
| Render timeout | Wall-clock time per render step | Disabled (`0`) | Rejection (`context.DeadlineExceeded`) |

## Construct & Function Cost Limits Reference

The following table maps individual CEL constructs, custom functions, and loop comprehensions to their static cost formulas, maximum safe inputs, and admission behavior:

| Construct / Operation | Example Expression | Cost Formula (Static & Runtime) | Maximum Safe Input | Admission Behavior If Exceeded |
|---|---|---|---|---|
| **Property Access** | `${metadata.name}`, `${parameters.port}` | Base AST traversal: 1–3 units | Any scalar property | **Admitted** (constant low cost) |
| **String Interpolation** | `"prefix-${metadata.name}-${val}"` | Base charge + sum of inner costs | Any string | **Admitted** (linear cost) |
| **FNV Hash (`oc_hash`)** | `${oc_hash(metadata.name)}` | $1 + \lceil 0.1 \times \text{bytes} \rceil$ | Input string $\le 1,024$ bytes | **Admitted** (output size = 8 chars) |
| **Map Merge (`oc_merge`)** | `${oc_merge(labels, overrides)}` | $1 + \sum \text{properties}(m_i)$ | Total properties $\le 2,000,000$ | **Rejected** if sum of map entries $> 2\text{M}$ |
| **DNS Label (`oc_dns_label`)** | `${oc_dns_label(metadata.name)}` | $1 + \text{bytes} + \lceil 0.1 \times \text{bytes} \rceil$ | Traversed string $\le 1,024$ bytes | **Admitted** (output capped at 63 chars) |
| **Resource Name (`oc_generate_name`)** | `${oc_generate_name(metadata.name, "svc")}` | $1 + \text{bytes} + \lceil 0.1 \times \text{bytes} \rceil$ | Traversed strings $\le 1,024$ bytes | **Admitted** (output capped at 253 chars) |
| **Field Omission (`oc_omit`)** | `${has(x) ? x : oc_omit()}` | Constant **1 unit** | N/A | **Admitted** (strips target field from JSON) |
| **Range Generator (`lists.range`)** | `${lists.range(100)}` | Generates list of integers | $N \le 10,000$ elements | **Rejected** if $N > 10,000$ (runtime error) |
| **Linear Map / Filter ($O(N)$)** | `${items.map(x, x + 1)}` | $C_{\text{base}} \times N$ units | $N \le 1,000$ (default bound) | **Admitted** ($\approx 15\text{–}200 \times N$ units depending on body complexity) |
| **Quadratic Nested Loop ($O(N^2)$)** | `${list.map(a, list.map(b, a + b))}` | $C_{\text{body}} \times N^2$ units | $N \le 100\text{–}365$ items | **Rejected** (403 Forbidden) when body cost $\times N^2 > 2\text{M}$ |
| **Cubic Nested Loop ($O(N^3)$)** | `${list.map(a, list.map(b, list.map(c, a+b+c)))}` | $C_{\text{body}} \times N^3$ units | $N \le 46$ items | **Rejected** (403 Forbidden) if $N > 46$ ($N=47 \implies 2.02\text{M}$) |
| **`forEach` Resource Loop** | `forEach: "${parameters.tags}"` | $\text{cost}(\text{expr}) + M \times C_{\text{body}}$ | Loop bound $M \le 100$ items | **Rejected** if $M > 100$ or $(M \times C_{\text{body}}) > 2\text{M}$ |
| **Whole-Template Cumulative** | Any CR template with multiple resources | $\sum \text{Resource Costs} \le 2,000,000$ | Total expansions $\le 10,000$ | **Rejected** (403 Forbidden) if cumulative cost $> 2\text{M}$ |
| **Unbounded Collection Iteration** | Comprehension over list without `maxItems` | Evaluates as `StateUnknown` ($2^{64}-1$) | Must define `maxItems` in OpenAPI | **Rejected** (403 Forbidden: `CEL cost verdict: UNKNOWN`) |

> [!NOTE]
> **Comprehension Cost Coefficients:** The static cost multiplier for CEL comprehensions (`map`, `filter`, `transformMap`) includes AST walking, intermediate variable allocation, and body evaluation. While a trivial arithmetic body (`x + 1`) charges $\approx 15$ units per item, bodies performing string operations, hashing, or property access charge significantly more (e.g. $\approx 220$ units per iteration in the quadratic test case). Template authors should not rely on a fixed constant coefficient; nested loops over collections with $N \ge 100$ must be avoided.
>
> **Workload CRD Bounds:** `Workload` collections enforce strict schema ceilings: `spec.container.env` $\le 1024$ items, `spec.container.files` $\le 256$ items, `spec.endpoints` $\le 128$ properties, and `spec.endpoints.visibility` $\le 4$ items. Pre-existing Workloads exceeding these limits will fail validation upon their next update.

## Per-expression cost limit

Every CEL program is compiled and run with `cel.CostLimit`. cel-go meters each evaluation
and aborts once the accumulated cost passes the limit, so no single expression can run
away regardless of how the inputs are shaped.

The 2,000,000 default was derived once, by measuring the most expensive expression in the
shipped ComponentTypes, ProjectTypes, ResourceTypes and workflows (~36,000 units) and
allowing two orders of magnitude of headroom, capped by what fits a controller's memory
budget. No test re-derives it; if a legitimate template starts tripping the limit after an
upgrade, raise it with `--cel-cost-limit` (below) and file the template as a data point.

## Cumulative cost budget

A per-expression limit alone is bypassed by volume: a large CR of individually cheap
expressions costs the same as one expensive one. `CostBudget` is a per-reconcile pool that
every evaluation charges against, seeded on the reconcile context by each rendering
reconciler and shared by everything that renders during that reconcile — manifests, outputs
and each `readyWhen`.

Because the budget is the cost limit × 1, it is the *tighter* of the two bounds: a
reconcile gets, in total, what one expression is allowed. When rendering happens outside a
reconcile (tests, tools), the pipeline falls back to a budget of its own so the bound is
never simply absent.

Once the budget is exhausted, further evaluations are refused before they run rather than
after. A concrete failure is still named ahead of the breach: if an expression fails on its
own terms, the operator sees that error, and the breach surfaces on the next evaluation.

## Custom-function cost accounting

cel-go charges an unknown overload roughly O(1). The `oc_*` functions
(`oc_hash`, `oc_generate_name`, `oc_dns_label`, `oc_merge`) walk tenant data, so an
`interpreter.ActualCostEstimator` charges them by bytes walked, feeding the same tracker as
the limit and the budget.

Dispatch is by function name, because `DynType` arguments block overload resolution.
**A new `oc_*` function ships unmetered unless it is added to
`customFunctionCostEstimator.CallCost` in `internal/template/custom_functions.go`.**

Interpolation — the non-CEL string work that stitches `${...}` results back into a template
string — is done in a single pass with a byte builder. That work is invisible to cel-go's
meter, so it must stay linear in input and output size.

## Expansion guards

Fan-out happens *between* evaluations, where neither the limit nor the budget is metered:
one cheap `forEach` expression can drive thousands of sub-renders.

- **forEach item cap (100 per loop).** Checked before any copying or fan-out, so an
  oversized collection costs only the length check. It applies to every `forEach` —
  resource-level, trait create, trait patch and trait remove alike — because all of them
  funnel through `renderer.ToIterableItems`. 100 covers what templates actually iterate
  over: a component's config files, mounts, routes and containers.
- **Cumulative forEach cap (10,000 per render).** Many legal 100-item loops recreate the
  same workload in total; this bounds their sum. The per-loop cap cannot stand in for it,
  because the number of forEach sites is a product of nested arrays — a ComponentType's
  `resources`, and the `creates` of each trait bound to the component — and bounding each
  array on its own leaves their product free to grow.
- **Patch operation cap (10,000 per component render).** Counts targets × operations, so a
  trait patching many targets is bounded even when each patch is small. The error names the
  running total the batch would have reached, which is what tells an operator whether they
  are marginally or wildly over.
- **`lists.range()` size cap (10,000 elements).** cel-go's own metering charges
  `lists.range` only *after* it allocates the whole slice, so the cost limit cannot
  interrupt the allocation. The cap is checked before it, and is therefore load-bearing.
  cel-go raises it as an ordinary evaluation error, not a sentinel.

## Render timeout

A wall-clock backstop for work the cost model measures imperfectly (`split()` on very large
strings is nonlinear, and whole-collection references charge roughly constant). It is
**disabled by default**; the cost guards are the primary bound.

It applies to each rendering *step*, not to the reconcile: a reconcile that renders more
than once (manifests, outputs, each `readyWhen`) may spend it at each. The deadline is
derived inside each render entry point and released when it returns, so nothing accumulates
across a loop. Cluster reads are deliberately left outside it — a slow API server should not
surface as a template failure.

## Failure surface

A breach is reported on the object's conditions, not only in the logs: `Synced=False` /
`ReleaseSynced=False` for manifest rendering, `OutputsResolved=False` for outputs,
`ResourcesReady=False` for `readyWhen`, and `WorkflowCompleted=False` with reason
`WorkflowRenderingFailed` for a WorkflowRun. Retries then follow each reconciler's
pre-existing behavior — controller-runtime's per-key exponential backoff. Every retry is
itself bounded by the same guards, so a repeating breach costs one cost-limited render.

Two properties make that safe:

**Messages are deterministic.** The recursive render walks map keys in sorted order and
stops at the first failing expression, so the expression named in the error does not follow
Go's randomized map iteration, and the breach message carries no live spend. An unchanged
object therefore produces a byte-identical condition on every reconcile, the status write
no-ops, and no watch event is generated — a failing object does not self-trigger a loop.

**Messages are bounded.** A tenant-authored expression may be hundreds of KB, and an
oversized or invalid-UTF-8 condition is rejected by the API server, which would mean the
breach is never recorded at all. Fragments are truncated at 256 bytes on a rune boundary,
with the marker counted inside the bound. Every layer that re-quotes tenant input routes
through the same helper (`template.TruncateFragment`), aggregated post-render failures are
capped at 20 entries, and `controller.NewCondition` bounds whatever it is handed at 8192
bytes as a backstop.

```go
// internal/controller/conditions.go
const maxConditionMessageLen = 8192
Message: template.TruncateTo(message, maxConditionMessageLen),
```

## Configuration

Settings are cluster-wide, applied to rendering reconcilers and admission webhooks.

| Flag | Environment variable | Helm value | Flag default | Chart default | Description |
|---|---|---|---|---|---|
| `--cel-cost-limit` | `CEL_COST_LIMIT` | `controllerManager.manager.celCostLimit` | `0` (built-in limit) | `2000000` | Runtime per-expression limit |
| `--render-timeout` | `RENDER_TIMEOUT` | `controllerManager.manager.renderTimeout` | `0` (disabled) | `"0"` | Render step wall-clock timeout |
| `--cel-cost-mode` | `CEL_COST_MODE` | `controllerManager.manager.celCostMode` | `warn` | `"warn"` | Static admission cost mode: `enforce`, `warn`, or `off` |

The runtime cost-limit defaults describe the same bound. `0` means "use the built-in limit",
and the chart states that limit — `defaultCELCostLimit` in `internal/template/engine_cache.go`
— outright, so an operator reading `values.yaml` sees the number rather than an indirection.

### Static Admission Cost Enforcement (Project 701)

Static worst-case CEL cost analysis runs during `ValidateCreate` and `ValidateUpdate` admission across template authoring resources:
- `ComponentType` & `ClusterComponentType` (`spec.resources[].template`, `spec.validations`)
- `Trait` & `ClusterTrait` (`spec.creates[].template`, `spec.patches[]`)
- `ResourceType` & `ClusterResourceType` (`spec.resources[].template`, `spec.outputs[].value`)
- `Workflow` & `ClusterWorkflow` (`spec.runTemplate`, `spec.resources[].template`)

Modes:
- **`enforce`**: Rejects templates with HTTP 403 Forbidden if static estimated cost exceeds the `2,000,000` render budget or evaluates as `UNKNOWN` due to unbounded/unmodeled input collections.
- **`warn`** (default): Admits the resource but returns an admission warning to `kubectl` when the estimate exceeds budget or evaluates as `UNKNOWN`.
- **`off`**: Bypasses static cost evaluation at admission time (relying solely on controller runtime backstops).

Updates to existing resources are evaluated using a per-resource ratchet: an update is admitted as long as it does not introduce higher violations on individual resource IDs (`VerdictWithinBudget (1) < VerdictOverBudget (2) < VerdictUnknown (3)`). The ordering reflects how little can be proved about the cost: `VerdictUnknown` is ranked worst because it carries no upper bound at all, whereas `VerdictOverBudget` still provides a bounded, deterministic static calculation.

## Known gaps

- `ProjectType` / `ClusterProjectType` does not have an admission webhook registered and relies on controller runtime render limits and timeouts.
- The authorization CEL runtime (`internal/authz`) is not covered by these guards.
- Whole-collection identifier references (`${parameters.foo}`, `oc_merge` arguments) charge roughly constant regardless of the collection's size.
- `forEach` loop variables (`res.Var`) are statically typed using the element `DeclType` of the `forEach` iterable expression. Variable names are author-chosen and fully invariant under renaming.
- Metadata and DNS strings are bounded via reflection and OpenAPI constraints (DNS labels bounded to 63, default string sizes bounded to 253), preventing unbounded string concatenation saturation. All shipped samples pass static analysis `WITHIN BUDGET` in `enforce` mode.

## How to verify

1. **CRD bounds match the markers**

```bash
make manifests PROJECT_DIR=. TOOL_BIN=/tmp/tools && git diff --exit-code config/crd/bases
```
Expected: command exits 0 with zero diff against `config/crd/bases`.

```bash
go test ./internal/validation/component/cost -run 'TestPlatformBounds_SyncWithWorkloadCRD|TestWorkload_SingleContainerInvariant'
```
Expected: `PASS` (all CRD marker bounds match `PlatformPolicyBounds` and single-container invariant holds).

2. **Estimator behavior**

```bash
go test ./internal/validation/component/cost -run 'TestStaticCost_|TestLoopVariableRenameInvariance|TestServiceComponentType'
```
Expected: `PASS`: quadratic over 1024 envs is OVER budget, linear over endpoints is WITHIN budget, renaming the loop variable doesn't change the verdict, and `service.yaml` has every resource KNOWN and WITHIN with non-zero per-resource cost.

3. **Admission modes (webhook)**

```bash
go test ./internal/webhook/componenttype ./internal/webhook/clustercomponenttype
```
Expected: `PASS`: `enforce` rejects over-budget and UNKNOWN with 403, `warn` admits with a warning, `off` skips evaluation.

4. **Boundary pair**

```bash
go test ./internal/validation/component/cost -run 'TestStaticCost_QuadraticRejection|TestStaticCost_LinearFilterEndpointsAdmission'
```
Expected: `PASS`: `TestStaticCost_LinearFilterEndpointsAdmission` admitted as `WITHIN BUDGET` (linear over 128 endpoints), `TestStaticCost_QuadraticRejection` rejected as `OVER BUDGET` (quadratic over 1024 envs).

5. **Whole package**

```bash
go vet ./internal/validation/component/...
```
Expected: command exits 0 with no diagnostic warnings.

```bash
go test -count=1 ./internal/validation/component/... ./internal/webhook/componenttype ./internal/webhook/clustercomponenttype
```
Expected: `PASS` across all packages without failures or race conditions.

### Cluster verification

Tested against kind cluster (`desktop-control-plane` v1.36.1) with `controllerManager.manager.celCostMode=enforce` and `warn`:

1. **Admit shipped sample without warnings (`enforce` mode):**
```bash
kubectl apply -f samples/getting-started/component-types/service.yaml
```
Output:
```text
clustercomponenttype.openchoreo.dev/service configured
```

2. **Admit cubic expression at true boundary edge ($N=46$, `enforce` mode):**
```bash
$n46 = @"
apiVersion: openchoreo.dev/v1alpha1
kind: Trait
metadata:
  name: test-cubic-n46
  namespace: default
spec:
  patches:
    - target:
        group: apps
        version: v1
        kind: Deployment
      operations:
        - op: add
          path: /metadata/annotations/cost
          value: "${[0,1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16,17,18,19,20,21,22,23,24,25,26,27,28,29,30,31,32,33,34,35,36,37,38,39,40,41,42,43,44,45].all(i, [0,1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16,17,18,19,20,21,22,23,24,25,26,27,28,29,30,31,32,33,34,35,36,37,38,39,40,41,42,43,44,45].all(j, [0,1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16,17,18,19,20,21,22,23,24,25,26,27,28,29,30,31,32,33,34,35,36,37,38,39,40,41,42,43,44,45].all(k, true)))}"
"@
$n46 | kubectl apply -f -
```
Output:
```text
trait.openchoreo.dev/test-cubic-n46 created
```

3. **Reject cubic expression at $N=60$ ($216,000$ operations $> 2\text{M}$ budget in static aggregator, `enforce` mode):**
```bash
$n60 = @"
apiVersion: openchoreo.dev/v1alpha1
kind: ComponentType
metadata:
  name: test-cubic-n60
  namespace: default
spec:
  workloadType: deployment
  parameters:
    openAPIV3Schema:
      type: object
      properties:
        list:
          type: array
          items:
            type: string
  resources:
    - id: deployment
      template:
        apiVersion: apps/v1
        kind: Deployment
        metadata:
          name: test
        spec:
          replicas: 1
          template:
            metadata:
              labels:
                app: test
            spec:
              containers:
                - name: app
                  image: test:latest
                  command: ${parameters.list.map(x, x)}
"@
$n60 | kubectl apply -f -
```
Output:
```text
Error from server (Forbidden): error when creating "STDIN": admission webhook "vcomponenttype-v1alpha1.kb.io" denied the request: ComponentType "test-cubic-n60" CEL cost verdict: OVER BUDGET (estimated cost 13631488 / budget 2000000)
```

4. **Reject ComponentType iterating unbounded parameters list (`enforce` mode):**
```bash
$unboundedCT = @'
apiVersion: openchoreo.dev/v1alpha1
kind: ComponentType
metadata:
  name: test-unbounded-ct
  namespace: default
spec:
  workloadType: deployment
  parameters:
    openAPIV3Schema:
      type: object
      properties:
        unboundedList:
          type: array
          items:
            type: string
  resources:
    - id: deployment
      template:
        apiVersion: apps/v1
        kind: Deployment
        metadata:
          name: test
        spec:
          replicas: 1
          template:
            metadata:
              labels:
                app: test
            spec:
              containers:
                - name: app
                  image: test:latest
                  command: ${parameters.unboundedList.map(a, parameters.unboundedList.map(b, parameters.unboundedList.map(c, a + b + c))).map(x, "run")}
'@
$unboundedCT | kubectl apply -f -
```
Output:
```text
Error from server (Forbidden): error when creating "STDIN": admission webhook "vcomponenttype-v1alpha1.kb.io" denied the request: ComponentType "test-unbounded-ct" CEL cost verdict: UNKNOWN (estimated cost 18446744073709551615 / budget 2000000)
```

5. **Reject Workload with 1025 env vars (CRD schema maxItems limit):**
```bash
$workload1025 | kubectl apply -f -
```
Output:
```text
The Workload "test-workload-1025" is invalid: 
* spec.container.env: Too many: 1025: must have at most 1024 items
* <nil>: Invalid value: null: some validation rules were not checked because the object was invalid; correct the existing errors to complete validation
```

6. **Admit Workload with 1024 env vars:**
```bash
$workload1024 | kubectl apply -f -
```
Output:
```text
workload.openchoreo.dev/test-workload-1024 created
```

7. **Switch to `warn` mode and observe admission warnings:**
```bash
kubectl -n openchoreo-control-plane set env deployment/controller-manager CEL_COST_MODE=warn
kubectl -n openchoreo-control-plane rollout status deployment controller-manager
```
Apply over-budget and UNKNOWN templates:
```text
Warning: ComponentType "test-overbudget-warn-ct" CEL cost verdict: OVER BUDGET (estimated cost 13631488 / budget 2000000)
componenttype.openchoreo.dev/test-overbudget-warn-ct created

Warning: ComponentType "test-unbounded-warn-ct2" CEL cost verdict: UNKNOWN (estimated cost 18446744073709551615 / budget 2000000)
componenttype.openchoreo.dev/test-unbounded-warn-ct2 created
```

8. **Component reconciliation verification:**
Apply admitted ComponentType + Component + Workload:
```text
componenttype.openchoreo.dev/simple-service created
component.openchoreo.dev/test-reconcile-comp created
workload.openchoreo.dev/test-reconcile-comp-workload created
```
Reconciliation status:
```text
status:
  conditions:
  - lastTransitionTime: "2026-10-18T15:21:48Z"
    message: Component validated successfully
    observedGeneration: 2
    reason: Reconciled
    status: "True"
    type: Ready
  observedGeneration: 2
```

