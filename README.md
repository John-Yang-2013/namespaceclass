# NamespaceClass

A Kubernetes operator that lets cluster admins define **namespace classes** — templates of
resources that are automatically provisioned into any namespace that references a class.

## Overview

`NamespaceClass` is a cluster-scoped CRD. Each class holds a list of arbitrary Kubernetes
resource templates (NetworkPolicies, ServiceAccounts, LimitRanges, ResourceQuotas, Roles, etc.).

When a namespace is labelled with `namespaceclass.akuity.io/name: <class-name>`, the controller
creates all resources defined in that class inside the namespace. If the label changes or the
class definition is updated, the controller automatically reconciles the difference.

## Use Cases

```
Admin defines two NamespaceClasses:
  public-network   → NetworkPolicy (allow all ingress) + ServiceAccount + LimitRange
  internal-network → NetworkPolicy (VPN-only) + ResourceQuota

Developer creates namespace "web-portal" with label:
  namespaceclass.akuity.io/name: public-network

Controller automatically creates all public-network resources inside web-portal.
```

## Architecture

- **CRD**: `NamespaceClass` (`platform.akuity.io/v1alpha1`) — cluster-scoped; `spec.resources` is a list of raw Kubernetes manifests (`runtime.RawExtension`)
- **Controller**: watches `Namespace` objects (primary) and `NamespaceClass` objects (secondary fan-out trigger)
- **Apply strategy**: Server-Side Apply (SSA) via `client.Apply` — idempotent and drift-correcting
- **Tracking**: every created resource is labelled `namespaceclass.akuity.io/managed-by=true` and `namespaceclass.akuity.io/class=<name>` for ownership tracking
- **Cleanup**: uses Kubernetes discovery API to scan all resource types, deletes orphans by label

## Getting Started

### Prerequisites
- go version v1.25.8+
- docker version 17.03+
- kubectl version v1.11.3+
- Access to a Kubernetes v1.11.3+ cluster

### Install CRDs into the cluster

```sh
make install
```

### Run the controller locally

```sh
make run
```

### Apply sample NamespaceClasses and Namespaces

```sh
kubectl apply -f config/samples/platform_v1alpha1_namespaceclass.yaml
```

This creates:
- `NamespaceClass/public-network` — NetworkPolicy + ServiceAccount + LimitRange
- `NamespaceClass/internal-network` — NetworkPolicy (VPN-only) + ResourceQuota
- `Namespace/web-portal` (uses `public-network`)
- `Namespace/internal-service` (uses `internal-network`)

Verify the controller provisioned resources:

```sh
kubectl get networkpolicy,serviceaccount,limitrange -n web-portal --show-labels
kubectl get networkpolicy,resourcequota -n internal-service --show-labels
```

### Switching a namespace to a different class

```sh
kubectl label namespace web-portal namespaceclass.akuity.io/name=internal-network --overwrite
```

The controller deletes `public-network` resources and creates `internal-network` resources.

### Updating a class

```sh
kubectl edit namespaceclass public-network
```

All namespaces using that class are automatically reconciled.

## Deploy to Cluster

**Build and push your image:**

```sh
make docker-build docker-push IMG=<registry>/namespaceclass:tag
```

**Deploy the manager:**

```sh
make deploy IMG=<registry>/namespaceclass:tag
```

**Or generate a single install YAML:**

```sh
make build-installer IMG=<registry>/namespaceclass:tag
kubectl apply -f dist/install.yaml
```

## Uninstall

```sh
kubectl delete -f config/samples/platform_v1alpha1_namespaceclass.yaml
make uninstall
make undeploy
```

## Development

```sh
make generate    # regenerate DeepCopy methods after editing *_types.go
make manifests   # regenerate CRD/RBAC YAML from markers
make test        # run unit tests
make lint-fix    # auto-fix linting issues
```

## License

Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
