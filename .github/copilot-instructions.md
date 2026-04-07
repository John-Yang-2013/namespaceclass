# GitHub Copilot Instructions — NamespaceClass Operator

## Project Overview

This is a Kubernetes operator built with **Kubebuilder v4 / controller-runtime**.
It introduces a `NamespaceClass` CRD that lets admins template resources into namespaces.

## Tech Stack

- **Go 1.25.8**
- **Kubebuilder v4** (kubebuilder CLI + controller-runtime framework)
- **controller-runtime v0.23.x** — reconciler, manager, SSA client
- **k8s.io/client-go** — discovery API for dynamic resource scanning
- **k8s.io/apimachinery** — `runtime.RawExtension`, `unstructured.Unstructured`

## Key Files

| File | Purpose |
|---|---|
| `api/v1alpha1/namespaceclass_types.go` | CRD Go type — `spec.resources []runtime.RawExtension` |
| `internal/controller/namespace_controller.go` | Main reconciler — watches Namespace + NamespaceClass |
| `cmd/main.go` | Manager bootstrap, wires DiscoveryClient + RESTMapper |
| `config/crd/bases/` | Generated CRD YAML — DO NOT edit manually |
| `config/rbac/role.yaml` | Generated RBAC — DO NOT edit manually |
| `config/samples/platform_v1alpha1_namespaceclass.yaml` | Example NamespaceClass + Namespace objects |

## Domain Labels & Annotations

```
namespaceclass.akuity.io/name          (label on Namespace)  — which class to use
namespaceclass.akuity.io/applied-class (annotation on Namespace) — last applied class (controller-managed)
namespaceclass.akuity.io/managed-by    (label on child resources) — ownership marker
namespaceclass.akuity.io/class         (label on child resources) — which class created this resource
```

## Reconcile Logic (namespace_controller.go)

The controller only reconciles **Namespace** objects. NamespaceClass changes fan-out into namespace reconcile requests via `EnqueueRequestsFromMapFunc`.

```
Reconcile(namespace):
  1. Fetch Namespace
  2. currentClass  = ns.labels["namespaceclass.akuity.io/name"]
  3. previousClass = ns.annotations["namespaceclass.akuity.io/applied-class"]
  4. If previousClass != currentClass → deleteResourcesForClass(previousClass)
  5. If currentClass != "" → applyResources(currentClass) + deleteOrphanedResources()
  6. Patch ns annotation applied-class = currentClass
```

**Apply strategy**: Server-Side Apply (`client.Apply` + `FieldOwner("namespaceclass-controller")`).
No manual fetch-and-compare needed — the API server handles drift correction.

**Orphan detection**: name-set diff between `spec.resources` names and live resources found via
discovery API filtered by `namespaceclass.akuity.io/class` label.

## Coding Conventions

- **Never edit** `zz_generated.deepcopy.go`, `config/crd/bases/`, `config/rbac/role.yaml` — regenerate with `make generate` / `make manifests`
- **Always run** `make generate && make manifests` after editing `*_types.go` or kubebuilder markers
- RBAC markers live in `namespace_controller.go` (not `main.go`)
- Logging follows Kubernetes conventions: capital first letter, no period, past tense errors
  ```go
  log.Info("Reconciling Namespace", "namespace", ns.Name)
  log.Error(err, "Failed to apply resource", "kind", obj.GetKind())
  ```
- Return `ctrl.Result{}, err` to requeue with backoff; `ctrl.Result{}, nil` when done

## Adding a New Feature

### Add a field to NamespaceClass spec
1. Edit `api/v1alpha1/namespaceclass_types.go`
2. Run `make generate && make manifests`
3. Update controller logic in `namespace_controller.go`

### Add another resource type to a sample class
Edit `config/samples/platform_v1alpha1_namespaceclass.yaml` — add a new entry under `spec.resources`.
Any valid Kubernetes manifest (any `apiVersion`/`kind`) is accepted.

### Test locally
```sh
make install   # installs CRD into current kubectl context
make run       # runs controller against current cluster
kubectl apply -f config/samples/platform_v1alpha1_namespaceclass.yaml
```

## Do Not

- Do not add a separate controller for `NamespaceClass` — it is reconciled indirectly via namespace fan-out
- Do not use typed clients for applying resources — use `unstructured.Unstructured` + dynamic client to support arbitrary resource kinds
- Do not use `ownerReferences` for tracking child resources — use the label strategy instead (owner refs are not queryable across kinds)
- Do not call `r.Get` before SSA `r.Patch(Apply)` — SSA is idempotent and the API server manages the diff
