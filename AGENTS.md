# ccluster — Agent Guide

Guidance for AI agents working in this repository. Read this before making changes;
several conventions here deliberately depart from the stock kubebuilder scaffold, and
following the generic kubebuilder advice instead will produce broken code.

For the project's goals and roadmap, see [README.md](README.md); for how the control plane
works and why, see [DESIGN.md](DESIGN.md). This file is about mechanics.

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
| `config/cp/`, `config/ip/` | Per-operator kustomize overlays. Not a deploy path any more — they are the **input** the Helm charts are generated from. |
| `config/crd/` | Generated CRDs. Referenced by the cp overlay only. |
| `charts/cp/chart/`, `charts/ip/chart/` | Per-operator Helm charts — how the operators are actually deployed. **Generated** from the overlays; see below. |
| `internal/argocd/v1alpha1/` | A hand-written **partial** mirror of Argo CD's `argoproj.io/v1alpha1` API — only the `Application` fields the operator sets or reads. Extend it by hand rather than reaching for `unstructured`. |
| `catalogs/` | The Helm chart catalog the operator deploys from. `catalogs/cluster_base/<component>` holds the 18 platform components. **Not part of the operator build**, and deliberately unrelated to `charts/`. |
| `catalog/` | Legacy Python/Pydantic packaging of an older copy of those charts. Being retired once the remaining charts are migrated to `catalogs/` — leave it alone. |

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

Finish with `make helm-generate`, or the new controller's RBAC will be missing from the
chart that actually gets deployed.

### The Helm charts are generated from the kustomize overlays

Both operators deploy via Helm. `charts/cp/chart/` and `charts/ip/chart/` are **generated**,
not hand-written: `kubebuilder edit --plugins=helm/v2-alpha` parses the kustomize output in
`dist/install-{cp,ip}.yaml` and emits the chart. `config/` therefore remains the single
source of truth for the manager Deployment, RBAC and CRDs, and controller-gen output cannot
drift from what is deployed.

```bash
make helm-generate        # config/ -> dist/install-{cp,ip}.yaml -> charts/{cp,ip}/chart
```

Run it after touching anything under `config/`, `api/`, or any `+kubebuilder:rbac` marker.
CI (`.github/workflows/test-chart.yml`) regenerates and fails on any diff, so stale charts
are caught in review.

The plugin's regeneration contract decides what you may hand-edit:

- **`Chart.yaml` and `values.yaml` are preserved** across regeneration. They are the only
  files in the charts you may edit, and the only safe home for the Argo CD dependency and
  the pinned `fullnameOverride`.
- **Everything under `templates/` is overwritten every run.** Do not put hand-written
  templates there; they will disappear. Carry additional resources in a separate chart.

Two sharp edges:

- `kubebuilder edit --plugins=helm/v2-alpha` records only **one** chart in `PROJECT` — the
  last invocation. `make helm-generate` therefore always passes explicit
  `--manifests` / `--output-dir` for both charts rather than relying on a bare
  `kubebuilder edit`, and runs cp last so `PROJECT` names the primary chart. A bare
  `kubebuilder edit` regenerates only cp.
- The plugin also injects single-operator `helm-deploy` / `helm-uninstall` / `install-helm`
  targets into the Makefile. Delete them — this repo suffixes every cluster-touching target
  `-cp` / `-ip`, and `install-helm` pipes a remote script into `bash`.

`fullnameOverride` is pinned to `ccluster-cp` / `ccluster-ip` in each chart's `values.yaml`
so resource names do not depend on the release name. `test/e2e/e2e_test.go` and the
`kubectl -n ccluster-cp-system scale deploy/ccluster-cp-controller-manager` recipe below
hardcode those names.

### Do not `make install` before `make deploy-cp`

The cp chart owns the CRDs, and Helm refuses to adopt resources that already exist without
its ownership metadata:

```
Error: CustomResourceDefinition "cclinfraplanes.ccluster.nvidia.com" exists and cannot be
imported into the current release: invalid ownership metadata; ...
```

This also hits any cluster that predates the Helm switch, since the old kustomize
`deploy-cp` applied the CRDs too. Hand them over to Helm — non-destructive, existing CRs are
untouched:

```bash
make helm-adopt-crds-cp   # adds the managed-by label and release annotations
make deploy-cp
```

Only reach for `make uninstall` instead if the cluster is disposable: deleting a CRD deletes
every custom resource of that kind.

`make install` is for the `make run-cp` loop, where no chart is installed — it is not a
prerequisite of `make deploy-cp`, which installs the CRDs itself.

Conversely `undeploy-cp` leaves the CRDs behind, so a `make uninstall` is needed to fully
reset a cluster. That is what `test/e2e` does.

### Argo CD is a chart dependency, never installed by the operator

The cp chart declares `argo-cd` (from `https://argoproj.github.io/argo-helm`) as a Helm
dependency, pinned in `charts/cp/chart/Chart.lock`. `make deploy-cp` installs it alongside
the operator.

The operator must **never** install or upgrade Argo CD. Creating Argo CD's CRDs and
ClusterRoles requires the operator to already hold those rights — Kubernetes prevents
privilege escalation — which would mean granting it effective cluster-admin permanently for
a one-time bootstrap; and an operator that owns Argo CD cannot be used to repair a broken
Argo CD. The operator's job is to create `Application` resources.

Argo CD is hub-and-spoke: **control-plane cluster only**. Infra-plane clusters get
registered there as remote destinations. The ip chart has no dependencies.

`make deploy-cp ARGOCD=false` skips it — for a cluster that already runs Argo CD, or for a
hermetic run that must not pull the Argo CD images. `test/e2e` uses both sides of that flag
deliberately: the default `make test-e2e` (quick tier) deploys with `ARGOCD=false`, which is
what makes `ApplicationCRDMissing` reachable and is why the CCLDeployment controller has to
tolerate the Application CRD being absent at startup — see
`CCLDeploymentReconciler.SetupWithManager`. `make test-e2e-full` (or `E2E_FULL=true`) deploys
with real Argo CD instead, to exercise an actual rollout. There is no longer a standalone
target that only checks the argoproj CRDs render: `make test-e2e-argocd` was removed, its
render-only assertions moved into `helm template` output in
`.github/workflows/test-chart.yml`, and the real rollout check moved into `make test-e2e-full`.
`make helm-deps-update-cp` re-resolves the dependency and refreshes
`Chart.lock`; that is how you bump the Argo CD version.

### The two controllers, and the invariants they rest on

Both live in `internal/controller/cp/`. [DESIGN.md](DESIGN.md) explains them properly; these
are the parts a well-meaning change breaks silently.

`CCLInfraPlaneReconciler` registers a cluster with Argo CD by mirroring the Secret named by
`spec.clusterCredentialsRef` into `--argocd-namespace`, under the CCLInfraPlane's own name.

`CCLDeploymentReconciler` turns each component of a `CCLDeployment` into one Argo CD
`Application`, in order.

- **Applications are written directly. There is no ApplicationSet.** Both an ApplicationSet
  and Argo CD's `RollingSync` were implemented and removed in `2e00925`; the reasoning is in
  the doc comments on `applicationSpec` in `ccldeployment_builder.go`. Do not reintroduce
  either.
- **Release is monotonic.** `releaseCount` withholds a component's Application until every
  earlier one is `Healthy` *and* `Synced`, but a released component is never withdrawn when
  it degrades — deleting its Application would uninstall the chart through the cascade
  finalizer.
- **Ownership is by annotation, never `ownerReference`.** Both controllers write into the
  Argo CD namespace, and owner references may not cross namespaces. Use the helpers in
  `ownership.go`; check `ownedBy` before touching or deleting anything there.
- **`ccluster.nvidia.com/wave` is a label for humans.** Nothing reads it. Ordering comes
  from the position of the component in the list.

### RBAC is generated per operator

`make manifests` runs `controller-gen` three times:

- CRDs once over `./api/...` → `config/crd/bases/`
- RBAC over `./internal/controller/cp/...` → `config/cp/rbac/role.yaml`
- RBAC over `./internal/controller/ip/...` → `config/ip/rbac/role.yaml` *(currently commented
  out in the Makefile)*

A `+kubebuilder:rbac` marker therefore only reaches the operator whose controller package it
sits in. Putting one in the wrong package silently grants the wrong operator.

The cp role currently covers both CRDs (plus their `status` and `finalizers` subresources),
core `secrets` — for the mirrored Argo CD cluster Secret — and `argoproj.io/applications`.
The ip role is empty.

The ip pass is disabled because `controller-gen` writes **no file at all** for a package with
zero markers, which breaks `kustomize build config/ip`. `config/ip/rbac/role.yaml` is
hand-written with `rules: []` for now. When the first IP controller lands: delete that file
and uncomment the ip line in the `manifests` target.

Both roles are named `manager-role`; the overlay `namePrefix` disambiguates them into
`ccluster-cp-manager-role` and `ccluster-ip-manager-role`, which is what lets the two
`role_binding.yaml` files be identical.

### Everything is per-operator: `-cp` / `-ip`

Bare `make deploy`, `make run`, and `make docker-build IMG=...` **do not exist** in their
single-operator form. Use the suffixed targets and the two image variables:

```bash
make build-cp            make build-ip
make run-cp              make run-ip
make docker-build-cp     make docker-build-ip      # one Dockerfile, --build-arg CMD=<binary>
make deploy-cp           make deploy-ip            # helm upgrade --install
make undeploy-cp         make undeploy-ip          # helm uninstall
make build-installer-cp  make build-installer-ip   # dist/install-{cp,ip}.yaml

IMG_CP ?= ccluster-cp-operator:dev
IMG_IP ?= ccluster-ip-operator:dev
```

`make install` / `make uninstall` (CRDs only), `make helm-generate` and `make helm-lint`
cover both operators at once, and so does `make test`.

Five unsuffixed targets drive the whole local loop, and each has `-cp` / `-ip` halves:

```bash
make init     # create both development Kind clusters (idempotent)
make build    # manifests, CRDs, deepcopy, both binaries, both images, both charts
make load     # build each image and load it into its own cluster
make deploy   # helm install cp into the cp cluster, ip into the ip cluster
make clean    # delete both clusters. Destructive, and does not ask
```

`make clean-all` goes further: both clusters plus `bin/` and `dist/`. Since `bin/` holds
the downloaded tools (controller-gen, kustomize, envtest, kubebuilder), the next build
after it is a slow one.

Three Kind cluster names, and they must not be confused. `KIND_CLUSTER_CP`
(`ccluster-cp`) and `KIND_CLUSTER_IP` (`ccluster-ip`) are the development clusters
`make init` creates and `load` / `deploy` act on. `KIND_CLUSTER_E2E`
(`ccluster-test-e2e`) belongs to the e2e targets, which **delete** it when they finish —
which is why it can never be one you are working in.

`make deploy` is the only target that names a cluster. `deploy-cp` and `deploy-ip` on
their own still act on the current kubeconfig context, so they can be aimed at a real
cluster; to aim them explicitly, set `KUBE_CONTEXT_CP` / `KUBE_CONTEXT_IP`, which become
`helm --kube-context`. That is the supported mechanism — `KUBECTL` is quoted in the
recipes, so `KUBECTL="kubectl --context foo"` does not work.

`build-installer-*` is no longer a deploy path: `dist/install-{cp,ip}.yaml` exist to feed
the chart generator. Deploy with `deploy-*`.

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

### `make undeploy-cp` keeps the CRDs, and takes Argo CD with it

The chart annotates every CRD `helm.sh/resource-policy: keep` (`crd.keep` in `values.yaml`),
so `helm uninstall` leaves the CRDs and therefore every custom resource in place. This was
*not* true of the old kustomize path, which included `../crd` and deleted them.

What `undeploy-cp` does remove is **Argo CD**, since it is part of the same release. To stop
just the operator, leaving Argo CD and everything it manages running:

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
- `charts/{cp,ip}/chart/templates/**` and `.helmignore` — from the kustomize overlays, via
  `make helm-generate`. Overwritten on every run.
- `charts/cp/chart/Chart.lock` — via `make helm-deps-update-cp`

**Modified by the build (expect it dirty in `git status`):**

- `config/{cp,ip}/kustomization.yaml` — `make build-installer-*` (and therefore
  `helm-generate`) rewrites the image via `kustomize edit set image`

**Hand-written, yours to edit:** everything else, including `config/base/manager.yaml`, all
other kustomization files, `config/samples/`, `config/ip/rbac/role.yaml` (for now), and —
the two exceptions inside the generated charts — `charts/{cp,ip}/chart/Chart.yaml` and
`charts/{cp,ip}/chart/values.yaml`.

## Workflows

**After editing `api/*_types.go` or any `+kubebuilder:` marker:**

```bash
make manifests generate   # regenerate CRDs + RBAC + deepcopy
make helm-generate        # propagate them into charts/{cp,ip}/chart
make install              # push the new CRD schema to the cluster
```

Skipping the first is the most common source of "my new field vanishes on apply" — the API
server prunes fields the installed CRD schema does not know about. Skipping the second means
the change is invisible to `make deploy-cp`, and CI will fail on the chart drift check.

This applies to **doc comments too**, not just fields: godoc on the API types is copied
verbatim into the CRD's `description`, so it ships to users through `kubectl explain`. A
comment edit that is not regenerated fails the CI drift check.

**Inner development loop** (fastest; manager runs on your host):

```bash
export KUBECONFIG=~/.kube/ccluster-dev.yaml
make install          # CRDs only — do not combine with `make deploy-cp`, see above
make run-cp
```

**Before committing:**

```bash
make fmt vet lint-fix
make test
make helm-generate && git status --short charts/   # must be clean; CI checks this
make helm-lint
```

**In-cluster verification** (the only loop that exercises real RBAC and the real image):

```bash
make load-cp                                   # builds the image, then loads it
kubectl -n ccluster-cp-system rollout restart deploy/ccluster-cp-controller-manager
```

`rollout restart` is required: `make deploy-cp` is a no-op when the Deployment spec and image
tag are unchanged, even though the image content changed.

## Testing

| Layer | Command | Covers | Does not cover |
| --- | --- | --- | --- |
| unit | `go test ./internal/controller/cp/...` | Pure rollout logic — `releaseCount`, `rolloutStatus`, `applicationsCondition`, `ownedBy`. Sub-second, no envtest binaries. | Anything touching the API server |
| envtest | `make test` | Controller logic against a real API server + etcd. No cluster needed. | RBAC, images, manifests |
| e2e (quick) | `make test-e2e` | Helm deploy with `ARGOCD=false` into a dedicated kind cluster (`ccluster-test-e2e`), created and destroyed by the target. Fast; runs on every PR. Covers RBAC (Secret mirroring, `Application` CRUD via a CRD-only trick) and the status paths reachable without Argo CD. | A real rollout — see the full tier |
| e2e (full) | `make test-e2e-full` | Same cluster, deployed with real Argo CD instead. A real catalog component reaching `Healthy`/`Synced`, multi-component ordered release, reverse teardown, and selfHeal drift correction — all under Argo CD's own reconciliation. Slower (real image pulls, multi-minute convergence waits); explicit only, not run in CI. | — |
| charts | `make helm-lint`, CI drift check | That the committed charts match `config/` and render, and that the vendored Argo CD dependency renders its CRDs and `controller.diff.server.side`. | Anything runtime |
| manual | `make run-cp` | Fast iteration on logic | **RBAC** — runs as your kubeconfig identity, not the operator ServiceAccount |

Only `make test-e2e` and a real in-cluster deploy will catch a missing `+kubebuilder:rbac`
marker. `make cleanup-test-e2e` tears down the e2e cluster if a run dies partway.

Test wiring lives in `internal/controller/cp/suite_test.go` (envtest setup, `k8sClient`).
Specs use Ginkgo/Gomega.

**Two tiers, deliberately.** Ginkgo/envtest is the primary one and where behaviour belongs.
Alongside it, `ccldeployment_builder_test.go` and `ownership_test.go` are plain table-driven
`go test` files in the same package. They run without `KUBEBUILDER_ASSETS` and without etcd —
`BeforeSuite` only fires under `RunSpecs` — so:

```bash
go test -run TestReleaseCount ./internal/controller/cp/     # ~0.4s, no setup
```

They exist for the states envtest cannot cheaply reach, not for coverage: a gap in the middle
of the wave sequence (Applications for components 1 and 3 but not 2) cannot be staged through
a reconciler that never creates out of order. Keep them selective. Do **not** add table tests
for one-line helpers that the existing cases already cover incidentally.

**Current state:** both cp controllers have real envtest suites —
`cclinfraplane_controller_test.go` and `ccldeployment_controller_test.go` — covering
mirroring and drift correction, ordered release, reverse teardown, pruning, ownership
conflicts, and the CEL validation rules, and both reconcilers are covered at ~89% of
statements. The specs are named after behaviour, so treat a failure as a broken contract
rather than a coverage regression. Note three things about how they run:

- No controller is started in the test manager. Every spec calls `Reconcile` directly, so
  the `Watches` map functions are exercised as plain functions and requeue behaviour is not.
- envtest runs no Argo CD, so `test/testdata/crd/argoproj.io_applications.yaml` is a
  schema-less stand-in for the `Application` CRD, and the specs write Application status
  themselves to drive a rollout forward.
- The remaining 0% is `SetupWithManager` on both reconcilers, left there on purpose: it is
  controller-runtime builder wiring whose real failure mode is a missing
  `+kubebuilder:rbac` marker, and only `make test-e2e` catches that.

`test/e2e` splits into two Ginkgo `Label()` tiers, selected by `-ginkgo.label-filter` from one
`E2E_FULL` Makefile variable (`test/e2e/e2e_test.go`'s `BeforeAll` reads the same variable to
decide whether `deploy-cp` gets `ARGOCD=false` or real Argo CD, so the fixture and the spec
selection never drift apart). A handful of specs carry no label and run in both tiers: the
manager comes up, both CRDs' RBAC surface not tied to Argo CD's presence (Secret mirroring and
`CCLInfraPlane` finalizer cleanup, and `CCLDeployment`'s `InfraPlaneNotFound` status path).

The **quick** tier (`make test-e2e`, the default, no `E2E_FULL` needed) deploys with
`ARGOCD=false` and adds two `Label("quick")` specs that need Argo CD genuinely absent:
`ApplicationCRDMissing` — the graceful-degradation path for a cluster where Argo CD isn't
installed yet — and a spec that renders just the Application CRD out of the vendored
`argo-cd` dependency chart with `helm template --show-only` (a static resource, no images to
pull), applies it, restarts the manager to pick up the watch, and asserts a real `Application`
gets created under the operator's own `argoproj.io/applications` RBAC. This is what makes the
quick tier fast enough to run on every PR while still exercising real RBAC.

The **full** tier (`make test-e2e-full`, or `make test-e2e E2E_FULL=true`) deploys real Argo CD
instead and adds three `Label("full")` specs: one registers the control-plane cluster with
itself, the same trick `config/samples/v1alpha1_cclinfraplane.yaml` documents, and drives one
catalog component all the way to `Healthy`/`Synced` under Argo CD's own reconciliation
(including an explicit check that argo-rollouts' ~600KB CRD resource itself reports `Synced`,
not just the aggregate Application — the concrete regression `ServerSideApply` and
`controller.diff.server.side` exist to prevent, DESIGN.md §6); one deploys two components in
order and proves both `releaseCount` withholding and `teardownNext`'s reverse, one-at-a-time
teardown against real convergence timing, not hand-written status; and one mutates a live
managed resource and confirms Argo CD's `selfHeal` reverts it. This tier is opt-in only — not
run in CI — because of the real image pulls and multi-minute convergence waits involved.

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
- **Status** uses `metav1.Condition`, not custom string fields. Prefer the standard types
  (`Available`, `Progressing`, `Degraded`) and add a specific one only where none fits — as
  `CCLDeployment` does with `Validated` and `ComponentsApplied`. Always set
  `observedGeneration`, on the condition and on `status.observedGeneration`.
- **Reconciliation** must be idempotent, and driven by `.Owns()` / `.Watches()` rather than
  polling with `RequeueAfter`. `SetControllerReference` is the normal way to get garbage
  collection — but it is unusable for anything the cp operator writes into the Argo CD
  namespace, since owner references may not cross namespaces. Those objects use the
  annotation back-references in `ownership.go` and an explicit finalizer instead.
- **API types** follow [Kubernetes API
  conventions](https://github.com/kubernetes/community/blob/master/contributors/devel/sig-architecture/api-conventions.md);
  prefer predefined types (`metav1.Time`, not `string`).

## Reference

- [DESIGN.md](DESIGN.md) — what the control plane does and why: the two CRDs, the ordering
  algorithm, and the alternatives that were tried and rejected.
- Kubebuilder book: https://book.kubebuilder.io — the general CLI, markers, and webhook
  documentation lives there and is not duplicated here. Where it conflicts with this file,
  **this file wins**, because the two-binary layout is not what the book assumes.
- Markers reference: https://book.kubebuilder.io/reference/markers.html
- controller-runtime FAQ:
  https://github.com/kubernetes-sigs/controller-runtime/blob/main/FAQ.md
