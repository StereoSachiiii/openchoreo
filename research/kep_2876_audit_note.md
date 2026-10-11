# How Kubernetes Implements Schema-Aware CEL Cost Estimation (KEP-2876)
## Research Findings and Upstream Code Audit for OpenChoreo (Project 701)

Project: Project 701 — Admission-Time Cost Validation for OpenChoreo Templates

 upstream implementation of KEP-2876 (CRD Validation Rules CEL Cost Estimation) and architectural mapping to OpenChoreo templates. Checked against pinned modules k8s.io/apiserver@v0.36.3, k8s.io/apiextensions-apiserver@v0.36.3, and github.com/google/cel-go@v0.31.0.

## 1. Summary

At CRD write time, the Kubernetes API server compiles each CEL rule against a type model (DeclType) built from the CRD OpenAPI structural schema. Every list, map, and string in that model carries a worst-case size: the declared maxItems, maxProperties, or maxLength when present; otherwise, a size derived from the 3 MiB request-size limit divided by the element minimum serialized size.

A cost estimator walks the DeclType tree for cel-go to calculate the worst-case cost of the expression. A rule placed inside an unbounded list is charged once per possible list element (rule cost = MaxCost x MaxCardinality). Rules exceeding a static limit (10,000,000) or exceeding the cumulative CRD limit (100,000,000) are rejected at admission time with guidance to add schema bounds.

At admission request time, the same cost units are metered at runtime, stopping evaluation if an expression exceeds PerCallLimit (1,000,000) or if the object exceeds RuntimeCELCostBudget (10,000,000). The static limits are 10x the runtime limits, acting as a static hygiene gate.

## 2. Limits and Calibration

| Constant | Value | What it limits | Pinned Upstream Source |
|---|---|---|---|
| PerCallLimit | 1,000,000 | Runtime cost per rule evaluation (roughly 0.1s) | k8s.io/apiserver/pkg/apis/cel/config.go:28 |
| RuntimeCELCostBudget | 10,000,000 | Runtime cost per custom resource (roughly 1s) | k8s.io/apiserver/pkg/apis/cel/config.go:34 |
| StaticEstimatedCostLimit | 10,000,000 | Static estimate per expression checked at CRD admission | k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/validation/validation.go:59 |
| StaticEstimatedCRDCostLimit | 100,000,000 | Sum of static estimates for all rules in one CRD version | k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/validation/validation.go:61 |
| MaxRequestSizeBytes | 3,145,728 (3 MiB) | Request-size assumption used for worst-case sizes | k8s.io/apiserver/pkg/apis/cel/config.go:40 |
| CheckFrequency | 100 | Iterations between interrupt checks in comprehensions | k8s.io/apiserver/pkg/apis/cel/config.go:29 |

The static limit (10,000,000) is 10x the runtime per-call limit (1,000,000), and the CRD cumulative static limit (100,000,000) is 10x the runtime object budget (10,000,000). The static limit is designed to catch O(n^2) or unbounded blowups while avoiding false rejections.

## 3. Upstream Code Instances: Where and How Cost Functions Are Called

### 3.1 Admission Validation Gate Entry Point

In k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/validation/validation.go:1275-1288, when a CRD is created or updated, the API server compiles and evaluates the static cost of each CEL validation rule:

```go
compResults, err := cel.Compile(typeInfo.Schema, typeInfo.DeclType, celconfig.PerCallLimit, opts.celEnvironmentSet, opts.preexistingExpressions)
if err != nil {
    allErrs.CELErrors = append(allErrs.CELErrors, field.InternalError(fldPath.Child("x-kubernetes-validations"), err))
} else {
    for i, cr := range compResults {
        expressionCost := getExpressionCost(cr, celContext)
        if !opts.suppressPerExpressionCost && expressionCost > StaticEstimatedCostLimit {
            costErrorMsg := getCostErrorMessage("estimated rule cost", expressionCost, StaticEstimatedCostLimit)
            allErrs.CELErrors = append(allErrs.CELErrors, field.Forbidden(fldPath.Child("x-kubernetes-validations").Index(i).Child("rule"), costErrorMsg))
        }
        if celContext.TotalCost != nil {
            celContext.TotalCost.ObserveExpressionCost(fldPath.Child("x-kubernetes-validations").Index(i).Child("rule"), expressionCost)
        }
    }
}
```

If the cumulative cost across the entire CRD exceeds 100,000,000, the CRD is rejected in validation.go:982-992:

```go
if celContext != nil && celContext.TotalCost != nil {
    if celContext.TotalCost.Total > StaticEstimatedCRDCostLimit {
        for _, expensive := range celContext.TotalCost.MostExpensive {
            costErrorMsg := fmt.Sprintf("contributed to estimated rule cost total exceeding cost limit for entire OpenAPIv3 schema")
            allErrs = append(allErrs, field.Forbidden(expensive.Path, costErrorMsg))
        }
        costErrorMsg := getCostErrorMessage("x-kubernetes-validations estimated rule cost total for entire OpenAPIv3 schema", celContext.TotalCost.Total, StaticEstimatedCRDCostLimit)
        allErrs = append(allErrs, field.Forbidden(fldPath.Child("openAPIV3Schema"), costErrorMsg))
    }
}
```

### 3.2 Cardinality and Overflow Multiplication

Rules attached inside lists or maps execute once per element. The API server calculates total expression cost by multiplying the expression MaxCost by the enclosing schema cardinality in validation.go:1340-1355:

```go
func multiplyWithOverflowGuard(baseCost, cardinality uint64) uint64 {
    if baseCost == 0 {
        return 0
    } else if math.MaxUint/baseCost < cardinality {
        return math.MaxUint
    }
    return baseCost * cardinality
}

func getExpressionCost(cr cel.CompilationResult, cardinalityCost *CELSchemaContext) uint64 {
    if cardinalityCost.MaxCardinality != unbounded {
        return multiplyWithOverflowGuard(cr.MaxCost, *cardinalityCost.MaxCardinality)
    }
    return multiplyWithOverflowGuard(cr.MaxCost, cr.MaxCardinality)
}
```

### 3.3 Rule Compilation and cel-go Cost Estimation

In k8s.io/apiextensions-apiserver/pkg/apiserver/schema/cel/compilation.go:236-258, compileRule configures the cel-go Program with CostTracking and runs EstimateCost:

```go
prog, err := ruleEnv.Program(ast,
    cel.CostLimit(perCallLimit),
    cel.CostTracking(estimator),
    cel.InterruptCheckFrequency(celconfig.CheckFrequency),
)
if err != nil {
    compilationResult.Error = &apiservercel.Error{Type: apiservercel.ErrorTypeInvalid, Detail: "program instantiation failed: " + err.Error()}
    return
}
costEst, err := ruleEnv.EstimateCost(ast, estimator)
if err != nil {
    compilationResult.Error = &apiservercel.Error{Type: apiservercel.ErrorTypeInternal, Detail: "cost estimation failed: " + err.Error()}
    return
}
compilationResult.MaxCost = costEst.Max
compilationResult.MaxCardinality = maxCardinality
compilationResult.Program = prog
```

## 4. How sizeEstimator and Size Derivation Are Implemented

### 4.1 AST Path Traversal Against DeclTypes

In compilation.go:306-348, newCostEstimator creates a sizeEstimator that resolves AST node paths directly by traversing the DeclType graph:

```go
func newCostEstimator(root *apiservercel.DeclType) *library.CostEstimator {
    return &library.CostEstimator{SizeEstimator: &sizeEstimator{root: root}}
}

type sizeEstimator struct {
    root *apiservercel.DeclType
}

func (c *sizeEstimator) EstimateSize(element checker.AstNode) *checker.SizeEstimate {
    if len(element.Path()) == 0 {
        return nil
    }
    currentNode := c.root
    // cut off "self" from path, since we always start there
    for _, name := range element.Path()[1:] {
        switch name {
        case "@items", "@values":
            if currentNode.ElemType == nil {
                return nil
            }
            currentNode = currentNode.ElemType
        case "@keys":
            if currentNode.KeyType == nil {
                return nil
            }
            currentNode = currentNode.KeyType
        default:
            field, ok := currentNode.Fields[name]
            if !ok {
                return nil
            }
            if field.Type == nil {
                return nil
            }
            currentNode = field.Type
        }
    }
    return &checker.SizeEstimate{Min: 0, Max: uint64(currentNode.MaxElements)}
}
```

Notice how cel-go comprehension iteration variables arrive at EstimateSize with synthetic path segments @items, @values, or @keys. The estimator transitions to currentNode.ElemType or currentNode.KeyType, allowing expressions like r.backends (where r iterates over routes) to correctly locate the nested field backend DeclType.

### 4.2 Size Derivation from Schema and 3 MiB Request Bound

In k8s.io/apiserver/pkg/cel/common/schemas.go:261-288, when maxItems, maxProperties, or maxLength are omitted, the API server calculates physical upper bounds based on the 3 MiB request limit:

```go
const maxRequestSizeBytes = int64(3 * 1024 * 1024) // 3 MiB

func estimateMaxArrayItemsFromMinSize(minSize int64) int64 {
    // subtract 2 to account for [ and ]
    return (maxRequestSizeBytes - 2) / (minSize + 1)
}

func estimateMaxAdditionalPropertiesFromMinSize(minSize int64) int64 {
    // 2 bytes for key + "" + colon + comma + smallest possible value
    keyValuePairSize := minSize + 6
    // subtract 2 to account for { and }
    return (maxRequestSizeBytes - 2) / keyValuePairSize
}

func estimateMaxElementsFromMaxLength(s Schema) int64 {
    // multiply the user-provided max length by 4 in the case of an otherwise-untyped string
    // OpenAPIv3 maxLength is specified in runes, but request limits are bytes (UTF-8 up to 4 bytes/rune)
    return zeroIfNegative(*s.MaxLength()) * 4
}
```

For schema nodes under unbounded parents, worst-case cardinality is derived in compilation.go:355-365:
```go
func maxCardinality(minSize int64) uint64 {
    sz := minSize + 1 // assume at least one comma between elements
    return uint64(celconfig.MaxRequestSizeBytes / sz)
}
```

And for objects, MinSerializedSize is computed in schemas.go:119-142:
```go
// an object will always be serialized at least as {}, so account for that
minSerializedSize := int64(2)
for name, prop := range s.Properties() {
    // only include required properties without a default value; default values are filled in server-side
    if required[name] && prop.Default() == nil {
        minSerializedSize += int64(len(name)) + fieldType.MinSerializedSize + 4
    }
}
objType.MinSerializedSize = minSerializedSize
```

### 4.3 Custom Function Modeling and Result Sizes

Kubernetes models extension functions in k8s.io/apiserver/pkg/cel/library/cost.go using a CostEstimator implementing both checker.CostEstimator (static) and interpretable.ActualCostEstimator (runtime).

When estimating calls, functions returning strings, lists, or maps must propagate ResultSize. If a function result size is omitted, cel-go assumes math.MaxUint64, causing downstream operations to saturate.

## 5. Updates and Ratcheting

In k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/validation/validation.go:
- Schema updates that do not modify a CRD version suppress the per-expression cost gate via suppressExpressionCostForUnchangedSchema:
  opts.suppressPerExpressionCost = true when opts.versionsWithUnchangedSchemas.Has(version) (validation.go:221-225).
- Unchanged rules are compiled with envSet.StoredExpressionsEnv() rather than NewExpressionsEnv() (validation.go:158-163), ensuring that rules valid under older versions continue to pass.

## 6. Mapping to OpenChoreo (Project 701)

| Kubernetes Concept | Upstream Kubernetes Mechanism | OpenChoreo Equivalent | OpenChoreo Architectural Adaptation |
|---|---|---|---|
| Rule context (self) | Bound to CRD schema DeclType | Tenant parameters & platform context | parameters and environmentConfigs use PE schema DeclType; workload and metadata use platform size model. |
| Static cost gate | 10M per rule / 100M per CRD | Per-expression and template cumulative budget | Per-expression budget 2,000,000 (engine_cache.go:61); cumulative render budget 2,000,000 (budget.go:126). |
| Cardinality multiplier | Product of enclosing maxItems | forEach template loop expansion | Per-resource forEach items capped at min(size, 100) (foreach.go:23); cumulative render cap 10,000 items (foreach.go:32). |
| DeclType AST walking | sizeEstimator walks DeclType via element.Path() | DeclType-walking sizeEstimator | Replace Structural AST walk in OpenChoreo with direct DeclType path traversal, supporting @items, @keys, @values. |
| String size model | UTF-8 rune to byte conversion (maxLength * 4) | UTF-8 byte conversion | oc_* runtime functions charge by bytes; static model uses maxLength * 4 bytes for consistency. |
| Custom function model | library.CostEstimator with ResultSize | customFunctionCostEstimator | Model oc_hash, oc_dns_label, oc_merge, oc_omit with explicit ResultSize to prevent math.MaxUint64 saturation. |
| Ratcheting on update | suppressPerExpressionCost for unchanged versions | Resource ID ratchet comparing old vs new | Old expressions re-estimated with current estimator; unchanged or cost-reduced expressions pass admission. |
