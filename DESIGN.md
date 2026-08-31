# CCluster design

How the CCluster control plane works today, and why it is built this way. For the project's
goals and roadmap see [README.md](README.md); for the mechanics of building, generating and
deploying it see [AGENTS.md](AGENTS.md).

This document describes what is implemented in `v1alpha1`, not what is planned. Where a
decision was made against an obvious alternative, the reasoning is recorded here so it does
not have to be rediscovered.

## 1. Problem and shape

CCluster manages a fleet: one **control-plane cluster** and any number of **infra-plane
clusters**. Platform components and workloads are Helm charts that have to land on the right
cluster, in the right namespace, with the right values — and, because a platform stack has
real dependencies (CRDs before the controllers that use them, cert-manager before anything
that wants a certificate), in the right *order*.

The control plane does not install charts itself. It runs **Argo CD** in a hub-and-spoke
topology — a single instance in the control-plane cluster, with every infra-plane cluster
registered there as a remote destination — and drives it by writing Argo CD API objects.
CCluster's operators are therefore a translation layer: they turn two small, declarative
custom resources into Argo CD `Application`s and cluster `Secret`s, and read Argo CD's health
back to decide what to do next.

### Why the operator never installs Argo CD

Argo CD is a **Helm dependency of the control-plane chart** (`charts/cp/chart/Chart.yaml`,
pinned in `Chart.lock`), installed alongside the operator by `make deploy-cp`. The operator
must never install or upgrade it, for two reasons:

- **Privilege escalation.** Creating Argo CD's CRDs and ClusterRoles requires the creator to
  already hold those rights — Kubernetes refuses to let a subject grant permissions it does
  not have. An operator that bootstraps Argo CD would need effective cluster-admin
  permanently, to perform a one-time action.
- **Recoverability.** An operator that owns Argo CD cannot be used to repair a broken
  Argo CD.

Infra-plane clusters run no Argo CD of their own. The ip chart has no dependencies.

## 2. Component map

```
  control-plane cluster
  ─────────────────────────────────────────────────────────────────────────

    CCLInfraPlane                        ┌──────────────────────┐
    + credentials Secret  ───────────▶   │                      │
                                         │ ccluster-cp-operator │
    CCLDeployment                        │                      │
    components[0..n]      ───────────▶   └───────┬──────────────┘
                                                 │
                        writes cluster Secrets   │   ▲
                        and one Application      │   │ reads Application
                        per released component   │   │ health and sync
                                                 ▼   │
                                         ┌───────────┴──────────┐
                                         │       Argo CD        │
                                         └───────────┬──────────┘
  ─────────────────────────────────────────────────  │  ──────────────────
                                                     │ syncs charts
                                                     │ from catalogs/
                            ┌────────────────────────┴───────────┐
                            ▼                                    ▼
                   infra-plane cluster A               infra-plane cluster B
```

| Piece | Role |
| --- | --- |
| `ccluster-cp-operator` | Runs in the control-plane cluster. Owns both controllers and all real logic. |
| `ccluster-ip-operator` | Runs in each infra-plane cluster. A placeholder: it registers no controllers yet. |
| Argo CD | Control-plane cluster only. Does every install, upgrade and uninstall. |
| `catalogs/` | The Helm chart catalog, in this Git repository. `catalogs/cluster_base/<component>` holds the 18 platform components. |
| `charts/{cp,ip}/chart` | How the operators themselves are deployed. Unrelated to `catalogs/`. |

The two operators are separate binaries from one Go module because they run in **different
clusters**, and therefore need separate images, RBAC and deployment overlays.

## 3. `CCLInfraPlane` — registering a cluster

```yaml
apiVersion: ccluster.nvidia.com/v1alpha1
kind: CCLInfraPlane
metadata:
  name: ccluster-local
  namespace: default
spec:
  clusterCredentialsRef:
    name: ccluster-local-credentials
```

A `CCLInfraPlane` names one Kubernetes cluster that CCluster may deploy to. Its only field
points at a `Secret` in the **same namespace** holding that cluster's Argo CD credentials —
the usual `server`, `config`, and optionally `tlsClientConfig` keys.

`CCLInfraPlaneReconciler` (`internal/controller/cp/cclinfraplane_controller.go`) mirrors that
Secret into the Argo CD namespace (`--argocd-namespace`) under the **CCLInfraPlane's own
name**, labelled `argocd.argoproj.io/secret-type: cluster`, which is how Argo CD discovers
clusters. Every key is copied verbatim, except one:

```go
mirror.Data[argoCDClusterNameKey] = []byte(infraPlane.Name)   // "name"
```

`data["name"]` is always overwritten, because that key *is* the cluster's identity inside
Argo CD, and it is what `CCLDeployment` components address a destination by. Letting the
source Secret set it would break the link between the two CRDs.

This also covers the control-plane cluster itself: a Secret whose `server` is exactly
`https://kubernetes.default.svc` makes Argo CD connect using its own ServiceAccount. Such a
mirror does not sit *alongside* Argo CD's built-in `in-cluster` entry — it defines that entry
under the CCLInfraPlane's name.

### Ownership by annotation, not `ownerReference`

The mirrored Secret lives in the Argo CD namespace; the `CCLInfraPlane` may live anywhere.
Owner references cannot cross namespaces — the garbage collector would look for the owner in
the Secret's own namespace, fail to find it, and delete the Secret. So ownership is recorded
as a back-reference annotation pair instead (`internal/controller/cp/ownership.go`):

```
ccluster.nvidia.com/infraplane-name
ccluster.nvidia.com/infraplane-namespace
```

`ownedBy` treats an object carrying **neither** annotation as adoptable, and one carrying a
*different* back-reference as foreign. The asymmetry is deliberate: adopting an unclaimed
object lets the operator recover from a partial write, while refusing a claimed one is what
stops two same-named CCLInfraPlanes in different namespaces from fighting over a single
Secret. A foreign object produces `Available=False/ClusterSecretConflict` rather than an
error — retrying would not help.

Because there is no owner reference, there is no garbage collection either, so the finalizer
`ccluster.nvidia.com/argocd-cluster-secret` deletes the mirror on delete — and only if the
ownership check still agrees it is ours.

### Status

`Available` — `True/ClusterSecretCreated`, or `False` with `SecretNotFound` (the referenced
Secret does not exist yet) or `ClusterSecretConflict`. Neither failure requeues: the
controller watches Secrets, so the fix re-triggers it.

## 4. `CCLDeployment` — an ordered bundle of charts

```yaml
apiVersion: ccluster.nvidia.com/v1alpha1
kind: CCLDeployment
metadata:
  name: ccluster-local-base
  namespace: default
spec:
  components:
    - name: argo-rollouts
      infraPlaneRef:
        name: ccluster-local
      chart: catalogs/cluster_base/argo_rollouts
      namespace: argo-rollouts
    - name: keda
      infraPlaneRef:
        name: ccluster-local
      chart: catalogs/cluster_base/keda
      namespace: keda
      values:
        keda:
          operator:
            replicaCount: 1
```

Each component is one Helm chart on one cluster:

- **`chart` is a path, not a chart name.** The catalog is a Git repository, so the generated
  Application uses `source.path`; `source.chart` would make Argo CD look for a chart of that
  name in a Helm registry. The repository and revision are **operator configuration**
  (`--catalog-repo-url`, `--catalog-revision`), not per-component fields, so every component
  of every `CCLDeployment` deploys from one catalog at one revision. A chart edit is
  therefore invisible until it is *pushed* to that revision — Argo CD's repo-server clones
  the repository itself.
- **`infraPlaneRef` is same-namespace**, and the CCLInfraPlane must already be `Available`,
  because the Application addresses its destination **by name** and that name only exists
  once the cluster Secret has been mirrored.
- **`values` passes through untouched.** It is `apiextensionsv1.JSON` with
  `x-kubernetes-preserve-unknown-fields`, forwarded as Argo CD's `helm.valuesObject` — so it
  travels as structured data rather than a marshalled YAML string. The operator neither
  interprets it nor validates it against the chart.

CEL validation enforces unique component names, at most 50 components, and
`metadata.name` ≤ 63 characters — the name is used as a label value on the generated
Applications.

Ordering is **positional** rather than a `dependsOn` graph because nothing yet needs to
express one. If that changes, the existing list order remains a valid topological order, so
adding dependencies will not invalidate objects written today.

### The generated Application

One Argo CD `Application` per released component, named `{deployment}-{component}`, in the
Argo CD namespace (`ccldeployment_builder.go`):

```go
Project: "default",
Source: { RepoURL: <--catalog-repo-url>, TargetRevision: <--catalog-revision>,
          Path: component.Chart, Helm: {ValuesObject: component.Values} },
Destination: { Name: component.InfraPlaneRef.Name, Namespace: component.Namespace },
SyncPolicy: { Automated: {Prune: true, SelfHeal: true},
              SyncOptions: ["CreateNamespace=true", "ServerSideApply=true"] },
```

plus the `resources-finalizer.argocd.argoproj.io` finalizer, without which deleting an
Application is non-cascading: Argo CD's record disappears and the chart stays installed and
unmanaged on the infra-plane cluster.

`spec` is rebuilt from scratch on every reconcile, so hand edits are reverted rather than
merged. Ownership uses the same annotation scheme as above, with
`ccluster.nvidia.com/deployment-{name,namespace}`; the same pair is also written as *labels*,
which is what the controller selects on to find the Applications it has already released.

### Why not an ApplicationSet, and why not RollingSync

Both were implemented and then removed (commit `2e00925`).

An **ApplicationSet** earns its keep when the set is *derived* from something the operator
does not already know — clusters, Git directories, a matrix. A `CCLDeployment` enumerates its
components explicitly, so a generator would only interpose a Go-template layer between the
operator and an object it already knows in full, and would force values through a templated
string instead of structured data.

Argo CD's **RollingSync** strategy gates Applications by switching automated sync *off* and,
as of v3.5.1, never switching it back on — which is incompatible with the `selfHeal` that
these Applications depend on for drift correction.

## 5. Ordering: release by withholding

The rollout order is enforced by **not creating** an Application until its predecessors are
converged, rather than by creating them all and holding their syncs. `releaseCount`
(`ccldeployment_builder.go`):

1. Count the leading run of components that already have an Application — that is what has
   been released. Zero released means release the first: it has nothing to wait on.
2. If *any* released component is not both `Healthy` and `Synced`, release no more. Checking
   all of them, not just the newest, means a component that degrades later also halts the
   rollout, which is what a sequential dependency implies.
3. Otherwise release exactly one more.

Three properties follow:

- **Stateless.** The rollout position is derived entirely from which Applications exist, so
  it needs no bookkeeping of its own and survives an operator restart.
- **Monotonic.** A released component is never withdrawn because it became unhealthy.
  Deleting its Application would, through the cascade finalizer, *uninstall the chart* — a
  transient health blip must not tear down a running component.
- **Unbounded.** There is no timeout. A component that never becomes healthy stalls
  everything after it; the `Progressing` condition names it, which is the only way a stuck
  rollout is visible.

Terminating Applications are excluded from this reasoning (`liveApplications`): one still
holds its cascade finalizer while Argo CD uninstalls the chart, and counting it would let a
pruned component hold the rollout open, or make a re-added one look already deployed.

Components removed from the spec are pruned *after* the writes, so a rename never leaves the
cluster with neither the old nor the new Application.

### Teardown runs in reverse, one at a time

Components are deployed in order because a later one may depend on an earlier one, so they
must come down the other way round: a component owning CRDs has to outlive whatever still
holds custom resources of that kind. Issuing the deletes in reverse is not enough — Argo CD
would uninstall them concurrently and the order would be lost. So `teardownNext` deletes
**exactly one** Application per reconcile, highest position first, and waits for it to be
gone (requeuing every 15s) before starting the next. Only when none remain is the
`ccluster.nvidia.com/argocd-applications` finalizer dropped.

## 6. Why `ServerSideApply` is mandatory

`ServerSideApply=true` on every Application, and `controller.diff.server.side: "true"` on the
Argo CD application controller (set in `charts/cp/chart/values.yaml`), are a pair — one
changes how Argo CD *writes* a resource, the other how it *compares* one. Both are required.

Argo CD's default client-side apply records the manifest in the
`kubectl.kubernetes.io/last-applied-configuration` annotation and diffs against it. An object
larger than the annotation size limit cannot carry it — argo-rollouts' `rollouts.argoproj.io`
CRD is around 600KB, and several other `cluster_base` charts are comparable — so the recorded
copy is truncated and the diff never converges. The resource then reports `OutOfSync`
forever despite syncing perfectly well, and since release requires `Healthy` **and**
`Synced`, one such CRD stalls every later component.

## 7. Status and observability

| CRD | Condition | Meaning |
| --- | --- | --- |
| `CCLInfraPlane` | `Available` | The cluster Secret has been mirrored. Reasons: `ClusterSecretCreated`, `SecretNotFound`, `ClusterSecretConflict`. |
| `CCLDeployment` | `Validated` | Every `infraPlaneRef` resolves to an `Available` CCLInfraPlane. Reasons: `ComponentsAccepted`, `InfraPlaneNotFound`, `InfraPlaneNotReady`. |
| `CCLDeployment` | `ComponentsApplied` | The Applications for the released components have been written. Reasons: `ApplicationsCreated`, `ApplicationCRDMissing`, `ApplicationConflict`, `ApplicationFailed`. |
| `CCLDeployment` | `Progressing` | `True/WaveSyncing` while the rollout is advancing, with a message naming the component being waited on; `False/RolloutComplete` when every component is healthy and synced. |

`ApplicationCRDMissing` is reported rather than returned as an error: Argo CD may simply not
be installed yet (`make deploy-cp ARGOCD=false`, or the operator started first), and no
amount of retrying fixes it.

Printer columns: `Available` on `CCLInfraPlane`; `Applied` and `Progress` on `CCLDeployment`.
Each Application also carries `ccluster.nvidia.com/wave`, a 1-based label that is purely
informational — nothing reads it — so that

```bash
kubectl get applications -n argocd -L ccluster.nvidia.com/wave
```

shows the order the operator is working through.

### Watches

Neither controller uses `Owns()`, because owner references cannot cross namespaces. Both use
`Watches` with map functions instead:

- `CCLInfraPlane` watches `Secret`s. A Secret in the Argo CD namespace carrying the
  back-reference annotations maps straight to its CCLInfraPlane; otherwise the controller
  lists CCLInfraPlanes in the Secret's namespace and matches `clusterCredentialsRef`.
- `CCLDeployment` watches `CCLInfraPlane`s (so a cluster becoming `Available` releases the
  deployments waiting on it) and `Application`s (so a component becoming healthy releases the
  next one).

There is no `RequeueAfter` polling anywhere except the 15s teardown retry.

## 8. Not built yet

- **`dependsOn`.** Ordering is a straight line; there is no dependency graph and no
  parallelism between independent components.
- **No rollout timeout.** A component that never converges blocks the rest indefinitely.
- **One catalog per operator.** `--catalog-repo-url` and `--catalog-revision` are global, so
  a single control plane cannot mix catalogs or revisions.
- **The infra-plane operator is empty.** `internal/controller/ip/` contains only `doc.go`;
  its RBAC role is hand-written with `rules: []`.
- **No cache scoping.** The manager caches all Secrets cluster-wide.
- **Test coverage.** `internal/controller/cp` has substantial envtest suites, but they invoke
  `Reconcile` directly rather than running the controllers in the test manager, so the
  watches and map functions are exercised only as plain functions. `test/e2e` installs real
  Argo CD and exercises both CRDs under the operator's real ServiceAccount RBAC, including a
  spec that registers the control-plane cluster with itself and drives a real catalog
  component to `Healthy`/`Synced`, so the `argoproj.io/applications` RBAC rule and a genuine
  rollout are both covered now — something `make test-e2e-argocd` never actually asserted
  before it was retired; it only checked that the argoproj CRDs existed. What that move traded
  away is coverage of `ApplicationCRDMissing`, the status path for a cluster where Argo CD
  genuinely isn't installed: `test-e2e` no longer reaches it, so it is asserted at the unit
  tier instead (`ccldeployment_builder_test.go`).
