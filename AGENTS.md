# ccluster — Agent Guide

Guidance for AI agents working in this repository. Read this before making changes;
several conventions here deliberately depart from the stock kubebuilder scaffold, and
following the generic kubebuilder advice instead will produce broken code.

For the project's goals and roadmap, see [README.md](README.md). This file is about
mechanics.

## What this repository contains

Two Kubernetes operators built from **one Go module**, plus a catalog of Helm charts.

| Path | What it is |
| --- | --- |
| `cmd/ccluster-cp-operator/` | **Control-plane operator.** Runs in the control-plane cluster. Owns essentially all real logic. |
| `cmd/ccluster-ip-operator/` | **Infra-plane operator.** Runs in each infra-plane cluster. Placeholder — registers no controllers yet. |
| `internal/manager/` | Manager bootstrap shared by both binaries: flags, logging, metrics + webhook servers, health probes, `mgr.Start`. |
| `internal/controller/cp/` | Controllers owned by the control-plane operator. |
| `internal/controller/ip/` | Controllers owned by the infra-plane operator. Currently empty (`doc.go` only). |
| `api/v1alpha1/` | CRD Go types. API group is the bare domain `ccluster.nvidia.com` — there is no group segment. |
| `config/base/` | Manager Deployment, metrics service, service account, leader-election and metrics RBAC. Consumed by **both** overlays. |
| `config/cp/`, `config/ip/` | Per-operator deploy overlays. |
| `config/crd/` | Generated CRDs. Referenced by the cp overlay only. |
| `catalog/` | Helm charts and typed value schemas. **Not part of the operator build.** See `catalog/README.md`. |

The two operators run in **different clusters**, which is why they have separate images,
separate RBAC, and separate overlays rather than one manager behind a feature flag.

There is **no** `cmd/main.go`, **no** `config/default/`, **no** `config/manager/`, and **no**
top-level `config/rbac/`. Any instruction referencing those paths is stale.

## Conventions that will bite you

These are the things you cannot infer from the file tree.

### Wiring a new controller is manual

`kubebuilder create api --controller=true` scaffolds the API and controller files correctly,
but it can only inject the `SetupWithManager` call into `cmd/main.go` — which this project
does not have. After scaffolding you must, by hand:

1. Move the reconciler into `internal/controller/cp/` or `internal/controller/ip/` and fix
   its package name.
2. Add it to the `setupControllers` function in the corresponding `cmd/*/main.go`.

The `// +kubebuilder:scaffold:*` markers live in `cmd/ccluster-cp-operator/main.go`. Do not
delete them.

### RBAC is generated per operator

`make manifests` runs `controller-gen` three times:

- CRDs once over `./api/...` → `config/crd/bases/`
- RBAC over `./internal/controller/cp/...` → `config/cp/rbac/role.yaml`
- RBAC over `./internal/controller/ip/...` → `config/ip/rbac/role.yaml` *(currently commented
  out in the Makefile)*

A `+kubebuilder:rbac` marker therefore only reaches the operator whose controller package it
sits in. Putting one in the wrong package silently grants the wrong operator.

The ip pass is disabled because `controller-gen` writes **no file at all** for a package with
zero markers, which breaks `kustomize build config/ip`. `config/ip/rbac/role.yaml` is
hand-written with `rules: []` for now. When the first IP controller lands: delete that file
and uncomment the ip line in the `manifests` target.

Both roles are named `manager-role`; the overlay `namePrefix` disambiguates them into
`ccluster-cp-manager-role` and `ccluster-ip-manager-role`, which is what lets the two
`role_binding.yaml` files be identical.

### Everything is per-operator: `-cp` / `-ip`

Bare `make deploy`, `make run`, `make build`, and `make docker-build IMG=...` **do not exist**
in their single-operator form. Use the suffixed targets and the two image variables:

```bash
make build-cp            make build-ip
make run-cp              make run-ip
make docker-build-cp     make docker-build-ip      # one Dockerfile, --build-arg CMD=<binary>
make deploy-cp           make deploy-ip
make undeploy-cp         make undeploy-ip
make build-installer-cp  make build-installer-ip   # dist/install-{cp,ip}.yaml

IMG_CP ?= ccluster-cp-operator:dev
IMG_IP ?= ccluster-ip-operator:dev
```

`make install` / `make uninstall` (CRDs only) remain single, and `make test` covers both.

### Leader-election IDs must stay distinct

`cp.ccluster.nvidia.com` and `ip.ccluster.nvidia.com`, set in each `main.go`. Identical IDs
would make the two managers contend for one lease if they ever shared a cluster.

### Pin your kubeconfig before touching a cluster

Every cluster-touching Make target (`deploy-*`, `undeploy-*`, `install`, `uninstall`, `run-*`)
acts on **the current kubeconfig context**, with no confirmation. Development machines here
carry production contexts (EKS, GKE, teleport) in the default kubeconfig, and a `kind create
cluster` anywhere switches the current context out from under you.

```bash
kind get kubeconfig --name ccluster-dev > ~/.kube/ccluster-dev.yaml
export KUBECONFIG=~/.kube/ccluster-dev.yaml
```

Note `KUBECTL` is quoted in the Makefile, so `KUBECTL="kubectl --context foo"` does **not**
work — it tries to exec that whole string as one binary. Use `KUBECONFIG`.

### `make undeploy-cp` deletes the CRDs

`config/cp` includes `../crd`, so undeploying removes every CRD and therefore every custom
resource in the cluster. To stop the operator without data loss:

```bash
kubectl -n ccluster-cp-system scale deploy/ccluster-cp-controller-manager --replicas=0
```

Once finalizers exist, delete CRs *before* undeploying — a finalized CR with no controller
leaves the CRD stuck `Terminating`.

## Generated vs. hand-edited

**Regenerated — never hand-edit:**

- `config/crd/bases/*.yaml` — from `+kubebuilder:` markers in `api/`, via `make manifests`
- `config/cp/rbac/role.yaml` — from `+kubebuilder:rbac:` markers, via `make manifests`
- `**/zz_generated.*.go` — via `make generate`
- `PROJECT` — from `kubebuilder` CLI commands

**Modified by the build (expect it dirty in `git status`):**

- `config/{cp,ip}/kustomization.yaml` — `make deploy-*` rewrites the image via
  `kustomize edit set image`

**Hand-written, yours to edit:** everything else, including `config/base/manager.yaml`, all
other kustomization files, `config/samples/`, and `config/ip/rbac/role.yaml` (for now).

## Workflows

**After editing `api/*_types.go` or any `+kubebuilder:` marker:**

```bash
make manifests generate   # regenerate CRDs + RBAC + deepcopy
make install              # push the new CRD schema to the cluster
```

Skipping this is the most common source of "my new field vanishes on apply" — the API server
prunes fields the installed CRD schema does not know about.

**Inner development loop** (fastest; manager runs on your host):

```bash
export KUBECONFIG=~/.kube/ccluster-dev.yaml
make install
make run-cp
```

**Before committing:**

```bash
make fmt vet lint-fix
make test
```

**In-cluster verification** (the only loop that exercises real RBAC and the real image):

```bash
make docker-build-cp
kind load docker-image ccluster-cp-operator:dev --name ccluster-dev
kubectl -n ccluster-cp-system rollout restart deploy/ccluster-cp-controller-manager
```

`rollout restart` is required: `make deploy-cp` is a no-op when the Deployment spec and image
tag are unchanged, even though the image content changed.

## Testing

| Layer | Command | Covers | Does not cover |
| --- | --- | --- | --- |
| envtest | `make test` | Controller logic against a real API server + etcd. No cluster needed. | RBAC, images, manifests |
| e2e | `make test-e2e` | Full deploy into a dedicated kind cluster (`ccluster-test-e2e`), created and destroyed by the target. | — |
| manual | `make run-cp` | Fast iteration on logic | **RBAC** — runs as your kubeconfig identity, not the operator ServiceAccount |

Only `make test-e2e` and a real in-cluster deploy will catch a missing `+kubebuilder:rbac`
marker. `make cleanup-test-e2e` tears down the e2e cluster if a run dies partway.

Test wiring lives in `internal/controller/cp/suite_test.go` (envtest setup, `k8sClient`).
Specs use Ginkgo/Gomega.

**Current state:** `CCLInfraPlaneReconciler.Reconcile` is an empty scaffold stub, and the
existing spec only asserts that it returns no error. Reported coverage is therefore
meaningless — do not treat it as evidence of correctness.

## Image tags

Do not use a `:latest` tag for local images. Kubernetes defaults `imagePullPolicy` to `Always`
for `:latest`, so a locally built and `kind load`ed image is ignored and the pod lands in
`ImagePullBackOff`. Any other tag defaults to `IfNotPresent` and works. Hence the `:dev`
default in `IMG_CP` / `IMG_IP`.

## Code style

- **Logging** follows the [Kubernetes message style
  guide](https://github.com/kubernetes/community/blob/master/contributors/devel/sig-instrumentation/logging.md#message-style-guidelines):
  capitalized, no trailing period, past tense, object type named, balanced key-value pairs.
  `log.Info("Created Deployment", "name", deploy.Name)`.
- **Status** uses `metav1.Condition`, not custom string fields. Standard types: `Available`,
  `Progressing`, `Degraded`.
- **Reconciliation** must be idempotent. Re-fetch before update to avoid conflicts. Use
  `SetControllerReference` for garbage collection, and `.Owns()`/`.Watches()` rather than
  polling with `RequeueAfter`.
- **API types** follow [Kubernetes API
  conventions](https://github.com/kubernetes/community/blob/master/contributors/devel/sig-architecture/api-conventions.md);
  prefer predefined types (`metav1.Time`, not `string`).

## Reference

- Kubebuilder book: https://book.kubebuilder.io — the general CLI, markers, and webhook
  documentation lives there and is not duplicated here. Where it conflicts with this file,
  **this file wins**, because the two-binary layout is not what the book assumes.
- Markers reference: https://book.kubebuilder.io/reference/markers.html
- controller-runtime FAQ:
  https://github.com/kubernetes-sigs/controller-runtime/blob/main/FAQ.md
