# Adversarial Resource Test Manifests (Project 701 CEL Cost Enforcement)

This directory contains standalone manifests to demonstrate live admission rejections and boundary behaviors on a running Kubernetes / Kind cluster.

---

## 1. Static Cost Over-Budget Rejection (HTTP 403 Forbidden)

- **File:** [`research/adversarial_overbudget_componenttype.yaml`](file:///c:/Users/sachin%20lakshitha/openchoreo/research/adversarial_overbudget_componenttype.yaml)
- **Description:** A `ComponentType` defining a comprehension over a parameter list bounded to 1,024 elements. The static aggregator calculates $13,631,488$ operations, which exceeds the cluster render budget of $2,000,000$.
- **Command:**
  ```powershell
  kubectl apply -f research/adversarial_overbudget_componenttype.yaml
  ```
- **Observed Response:**
  ```text
  Error from server (Forbidden): error when creating "research/adversarial_overbudget_componenttype.yaml": admission webhook "vcomponenttype-v1alpha1.kb.io" denied the request: ComponentType "adversarial-overbudget" CEL cost verdict: OVER BUDGET (estimated cost 13631488 / budget 2000000)
  ```

---

## 2. Static Cost Unbounded / Unknown Rejection (HTTP 403 Forbidden)

- **File:** [`research/adversarial_unknown_componenttype.yaml`](file:///c:/Users/sachin%20lakshitha/openchoreo/research/adversarial_unknown_componenttype.yaml)
- **Description:** A `ComponentType` defining a 3-level nested comprehension over an unbounded collection without OpenAPI `maxItems`. The size evaluator saturates the 63-bit integer limit and returns `StateUnknown` with `VerdictUnknown`.
- **Command:**
  ```powershell
  kubectl apply -f research/adversarial_unknown_componenttype.yaml
  ```
- **Observed Response:**
  ```text
  Error from server (Forbidden): error when creating "research/adversarial_unknown_componenttype.yaml": admission webhook "vcomponenttype-v1alpha1.kb.io" denied the request: ComponentType "adversarial-unknown" CEL cost verdict: UNKNOWN (estimated cost 18446744073709551615 / budget 2000000)
  ```

---

## 3. Workload Schema Oversized Collection Rejection (OpenAPI Validation)

- **File:** [`research/adversarial_oversized_workload_1025.yaml`](file:///c:/Users/sachin%20lakshitha/openchoreo/research/adversarial_oversized_workload_1025.yaml)
- **Description:** A `Workload` resource defining $1,025$ environment variables. The API server OpenAPI schema validates `maxItems: 1024` on `spec.container.env` and rejects the object directly.
- **Command:**
  ```powershell
  kubectl apply -f research/adversarial_oversized_workload_1025.yaml
  ```
- **Observed Response:**
  ```text
  The Workload "adversarial-oversized-workload-1025" is invalid: 
  * spec.container.env: Too many: 1025: must have at most 1024 items
  * <nil>: Invalid value: null: some validation rules were not checked because the object was invalid; correct the existing errors to complete validation
  ```

---

## 4. Valid Workload Boundary (Admission)

- **File:** [`research/valid_bounded_workload_1024.yaml`](file:///c:/Users/sachin%20lakshitha/openchoreo/research/valid_bounded_workload_1024.yaml)
- **Description:** The counterpart boundary test defining exactly $1,024$ environment variables. Admitted cleanly.
- **Command:**
  ```powershell
  kubectl apply -f research/valid_bounded_workload_1024.yaml
  ```
- **Observed Response:**
  ```text
  workload.openchoreo.dev/valid-bounded-workload-1024 created
  ```

---

## 4b. Container Files Oversized Collection Rejection (OpenAPI Validation)

- **File:** [`research/adversarial_oversized_files_257.yaml`](file:///c:/Users/sachin%20lakshitha/openchoreo/research/adversarial_oversized_files_257.yaml)
- **Description:** A `Workload` resource defining $257$ files mounted into the container. The API server OpenAPI schema validates `maxItems: 256` on `spec.container.files` and rejects the object directly.
- **Command:**
  ```powershell
  kubectl apply -f research/adversarial_oversized_files_257.yaml
  ```
- **Expected Response:**
  ```text
  The Workload "adversarial-oversized-files-257" is invalid:
  * spec.container.files: Too many: 257: must have at most 256 items
  ```

---

## 4c. Workload Endpoints Oversized Map Rejection (OpenAPI Validation)

- **File:** [`research/adversarial_oversized_endpoints_129.yaml`](file:///c:/Users/sachin%20lakshitha/openchoreo/research/adversarial_oversized_endpoints_129.yaml)
- **Description:** A `Workload` resource defining $129$ exposed network endpoints. The API server OpenAPI schema validates `maxProperties: 128` on `spec.endpoints` and rejects the object directly.
- **Command:**
  ```powershell
  kubectl apply -f research/adversarial_oversized_endpoints_129.yaml
  ```
- **Expected Response:**
  ```text
  The Workload "adversarial-oversized-endpoints-129" is invalid:
  * spec.endpoints: Too many: 129: must have at most 128 items
  ```

---

## 4d. Dependency Endpoints Oversized Collection Rejection (OpenAPI Validation)

- **File:** [`research/adversarial_oversized_dep_endpoints_51.yaml`](file:///c:/Users/sachin%20lakshitha/openchoreo/research/adversarial_oversized_dep_endpoints_51.yaml)
- **Description:** A `Workload` resource defining $51$ consumed endpoint dependencies. The API server OpenAPI schema validates `maxItems: 50` on `spec.dependencies.endpoints` and rejects the object directly.
- **Command:**
  ```powershell
  kubectl apply -f research/adversarial_oversized_dep_endpoints_51.yaml
  ```
- **Expected Response:**
  ```text
  The Workload "adversarial-oversized-dep-endpoints-51" is invalid:
  * spec.dependencies.endpoints: Too many: 51: must have at most 50 items
  ```


## 5. Demonstrating `warn` Mode vs `enforce` Mode

Switch the running controller manager to `warn` mode:
```powershell
kubectl -n openchoreo-control-plane set env deployment/controller-manager CEL_COST_MODE=warn
kubectl -n openchoreo-control-plane rollout status deployment controller-manager
```

Now re-apply [`research/adversarial_overbudget_componenttype.yaml`](file:///c:/Users/sachin%20lakshitha/openchoreo/research/adversarial_overbudget_componenttype.yaml):
```powershell
kubectl apply -f research/adversarial_overbudget_componenttype.yaml
```
Output:
```text
Warning: ComponentType "adversarial-overbudget" CEL cost verdict: OVER BUDGET (estimated cost 13631488 / budget 2000000)
componenttype.openchoreo.dev/adversarial-overbudget created
```

Switch back to `enforce` mode:
```powershell
kubectl -n openchoreo-control-plane set env deployment/controller-manager CEL_COST_MODE=enforce
kubectl -n openchoreo-control-plane rollout status deployment controller-manager
```
