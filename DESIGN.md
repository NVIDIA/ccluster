# CCluster design

How the CCluster control plane works today, and why it is built this way. For the project's
goals and roadmap see [README.md](README.md); for the mechanics of building, generating and
deploying it see [Development](docs/development.md). Agent instructions are in
[AGENTS.md](AGENTS.md).

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
    CCLComponentSet                        │                      │
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
| `ccluster-ip-operator` | Runs in each infra-plane cluster. Registers no controllers yet; `CCLClusterConfig` (§3a) is the input its first one will reconcile. |
| Argo CD | Control-plane cluster only. Does every install, upgrade and uninstall. |
| `catalogs/` | The Helm chart catalog, in this Git repository. `catalogs/cluster_base/<component>` holds the 18 platform components, `catalogs/deployments/<chart>` the four tenant workload charts, `catalogs/resources/<chart>` the GPU quota queues and tenant volumes they use, and `catalogs/namespace_base` the tenant namespace baseline, each paired with a Go package describing that chart's values (§4). |
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
Secret into the Argo CD namespace (`--argocd-namespace`) under the name
**`<namespace>-<name>`**, labelled `argocd.argoproj.io/secret-type: cluster`, which is how
Argo CD discovers clusters. Cluster Secrets are the one thing Argo CD reads only from its own
namespace — there is no "clusters in any namespace" — so they stay there, and the prefix is
what keeps two namespaces' same-named planes apart. Every key is copied verbatim, except two:

```go
mirror.Data[argoCDClusterNameKey]    = []byte("<namespace>-<name>")   // "name"
mirror.Data[argoCDClusterProjectKey] = []byte("<namespace>")          // "project"
```

`data["name"]` is always overwritten, because that key *is* the cluster's identity inside
Argo CD, and it is what `CCLComponentSet` components address a destination by. Letting the
source Secret set it would break the link between the two CRDs. `data["project"]` makes the
cluster **project-scoped**: only Applications in that AppProject may deploy to it.

### One AppProject per namespace

Before it touches the Secret, the same reconciler writes an Argo CD `AppProject` into the
Argo CD namespace, **named after the CCLInfraPlane's namespace**
(`cclinfraplane_project.go`). Every CCLInfraPlane and CCLComponentSet in a namespace shares it;
it is the isolation boundary on the Argo CD side:

```go
SourceRepos:                     [<--catalog-repo-url>],          // the catalog, nothing else
SourceNamespaces:                [<namespace>],                   // Applications may live here
Destinations:                    [{Server: "*", Namespace: "*"}],
PermitOnlyProjectScopedClusters: true,                            // ...but only this namespace's clusters
ClusterResourceWhitelist:        [{Group: "*", Kind: "*"}],
```

`sourceNamespaces` is the project-side half of Argo CD's "Applications in any namespace"
feature; the cp chart supplies the controller-side half by setting
`application.namespaces: "*"` on `argocd-cmd-params-cm`. It is omitted when the namespace
*is* the Argo CD namespace, which Argo CD forbids granting and which needs no grant. The
project is created whenever a CCLInfraPlane exists in the namespace, credentials or not,
and deleted by the finalizer of the **last** non-terminating CCLInfraPlane in the namespace.
Terminating planes are not counted, so two deleted together both try the delete rather than
each deferring to the other.

The project exists before `Available` is ever set to true. That ordering is a contract:
`CCLComponentSetReconciler` writes Applications into the project without checking for it,
because an `Available` CCLInfraPlane in the namespace already guarantees it.

Naming the project after the namespace means CCLInfraPlanes in the `default` namespace adopt
and rewrite Argo CD's built-in `default` project — the same adopt-if-unclaimed rule as every
other object the operator owns — and the last one out deletes it. That is accepted: the
built-in project permits everything, which is exactly what a ccluster namespace's project
must not do.

If Argo CD is not installed, the AppProject CRD is missing along with the Application CRD.
The reconciler logs and carries on rather than failing: the CCLComponentSet controller reports
`ApplicationCRDMissing` in the same situation, and the restart that follows installing Argo
CD reconciles every CCLInfraPlane and creates the projects then.

This also covers the control-plane cluster itself: a Secret whose `server` is exactly
`https://kubernetes.default.svc` makes Argo CD connect using its own ServiceAccount. Such a
mirror does not sit *alongside* Argo CD's built-in `in-cluster` entry — it defines that entry
under the CCLInfraPlane's name.

### Ownership by annotation, not `ownerReference`

The mirrored Secret and the AppProject live in the Argo CD namespace; the `CCLInfraPlane`
may live anywhere. Owner references cannot cross namespaces — the garbage collector would
look for the owner in the object's own namespace, fail to find it, and delete the object. So
ownership is recorded as back-reference annotations instead
(`internal/controller/cp/ownership.go`). The Secret carries a pair naming its CCLInfraPlane;
the AppProject, which belongs to the namespace rather than to any one plane, carries only
the second:

```
ccluster.nvidia.com/infraplane-name
ccluster.nvidia.com/infraplane-namespace
```

`ownedBy` treats an object carrying **no** back-reference as adoptable, and one carrying a
*different* back-reference as foreign. The asymmetry is deliberate: adopting an unclaimed
object lets the operator recover from a partial write (and take over Argo CD's built-in
`default` project), while refusing a claimed one is what stops two CCLInfraPlanes whose
names collide from fighting over a single Secret — `<namespace>-<name>` is not
collision-free: `a-b`/`c` and `a`/`b-c` both yield `a-b-c`. A foreign object produces
`Available=False` with `ClusterSecretConflict` or `ProjectConflict` rather than an error —
retrying would not help.

Because there is no owner reference, there is no garbage collection either, so the finalizer
`ccluster.nvidia.com/argocd-cluster-secret` deletes the mirror, and the project if unused,
on delete — and only if the ownership check still agrees they are ours.

### Status

`Available` — `True/ClusterSecretCreated`, or `False` with `SecretNotFound` (the referenced
Secret does not exist yet), `ClusterSecretConflict` or `ProjectConflict`. No failure
requeues: the controller watches Secrets and AppProjects, so the fix re-triggers it.

## 3a. `CCLClusterConfig` — describing an infra-plane cluster from the inside

```yaml
apiVersion: ccluster.nvidia.com/v1alpha1
kind: CCLClusterConfig
metadata:
  name: cluster
spec:
  domain: gpu-a.infra.example.com
```

The control plane knows what it *asked* Argo CD to deploy onto a cluster, and Argo CD's
health tells it whether the sync landed. Neither says whether the platform stack on that
cluster actually works and is configured for it — whether SPIRE issues identities in the
cluster's own trust domain, whether external-dns is filtered to the cluster's zone. That
is the infra-plane operator's job, and it needs an input to check against.

`CCLClusterConfig` is that input, a cluster-scoped singleton (the CRD's CEL rule admits
only the name `cluster`) served by the ip chart alone: the control-plane chart drops it,
and the ip chart drops the two control-plane kinds, each with a `$patch: delete` in its
overlay, because kustomize will not load a CRD file from outside a kustomization's own
directory and so a per-operator list cannot live under `config/crd/`. Its spec carries
just what checks consume today: the cluster's domain, which is also its SPIFFE trust
domain. It is created once the domain exists — which is after the cluster has been
registered with the control plane. Its status reports a `Verified` condition; the
controller that writes it is not built yet (§8).

## 4. `CCLComponentSet` — an ordered bundle of charts

```yaml
apiVersion: ccluster.nvidia.com/v1alpha1
kind: CCLComponentSet
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
  of every `CCLComponentSet` deploys from one catalog at one revision. A chart edit is
  therefore invisible until it is *pushed* to that revision — Argo CD's repo-server clones
  the repository itself.
- **`infraPlaneRef` is same-namespace**, and the CCLInfraPlane must already be `Available`
  *for its current generation*, because the Application addresses its destination **by
  name** and that name only exists once the cluster Secret has been mirrored — and because
  `Available` is what guarantees the namespace's AppProject exists for the Application to
  belong to. The generation is part of it because a condition outlives the spec it was
  computed for: a plane whose `clusterCredentialsRef` has just changed still reports the
  previous spec's `Available` until its own controller catches up, and the cluster that
  condition described is no longer the one the ref names.
- **`values` passes through untouched.** It is `apiextensionsv1.JSON` with
  `x-kubernetes-preserve-unknown-fields`, forwarded as Argo CD's `helm.valuesObject` — so it
  travels as structured data rather than a marshalled YAML string. The operator neither
  interprets it nor validates it against the chart.

`chart`, `namespace` and `values` are grouped into a `ComponentEntry` struct, embedded into
`ComponentSpec` with `json:",inline"`. The embed is anonymous because `encoding/json` inlines
only anonymous fields, so the three stay component-level fields on the wire and the grouping
is purely a Go-side one. It exists so the catalog packages can produce a `ComponentEntry`
from a typed, per-chart values struct.

### The typed catalog, and what it does not guarantee

Each chart under `catalogs/` has a Go package
alongside it declaring its values as typed structs, plus a `New(namespace, values)` that
builds a `v1alpha1.ComponentEntry` from them — the API type itself, not a catalog-side
mirror of it. A caller building a `CCLComponentSet` in Go gets compile-time checking of the
values it sets. The `deployments` charts are written here rather than wrapped, so their
values are the chart's own document, they validate what the chart cannot (required values,
port and path rules), and they scope everything to the release namespace instead of
carrying a tenant identifier: the namespace is the tenant.

A component knows its chart directory and nothing else: its name, its namespace and its
position in the rollout are all the caller's, because they are deployment choices rather
than facts about the chart. `catalogs/cluster_base.Components(infraPlane)` writes down one
plausible set of all three — sample names and namespaces, chart defaults for every values
struct, and the rollout order carried over from the legacy Python catalog's sync waves — so that a caller has something to copy rather than an empty page.

**None of this is enforced at admission.** The CRD's `values` is as untyped as it ever was, so
a hand-written `CCLComponentSet` bypasses the typed layer entirely and is forwarded to Helm
verbatim. The concrete hazard is a chart with required values, such as `kai_gpu_queues`, whose
queues each need a name and a quota: a `CCLComponentSet` that omits them is accepted, and the
mistake surfaces only when Argo CD syncs the chart. The only check is a `Values` type's `Validate()`,
called by `catalogs.Entry` — which a caller only reaches by going through the Go layer.

The alternative, a discriminated union of every chart's schema inside `ComponentSpec`, would have
bought API-server rejection at the cost of putting every chart's schema into the CRD, where
changing one becomes a schema migration. Given §6's experience with a 600KB CRD, that trade
was not worth making yet.

CEL validation enforces unique component names, at most 50 components, and
`metadata.name` ≤ 63 characters — the name is used as a label value on the generated
Applications.

Ordering is **positional** rather than a `dependsOn` graph because nothing yet needs to
express one. If that changes, the existing list order remains a valid topological order, so
adding dependencies will not invalidate objects written today.

### The generated Application

One Argo CD `Application` per released component, named `{componentSet}-{component}`, **in
the CCLComponentSet's own namespace** — not the Argo CD namespace — under that namespace's
AppProject (`cclcomponentset_builder.go`):

```go
Project: <componentSet.Namespace>,
Source: { RepoURL: <--catalog-repo-url>, TargetRevision: <--catalog-revision>,
          Path: component.Chart, Helm: {ValuesObject: component.Values} },
Destination: { Name: "<componentSet.Namespace>-<component.InfraPlaneRef.Name>",
               Namespace: component.Namespace },
SyncPolicy: { Automated: {Prune: true, SelfHeal: true},
              SyncOptions: ["CreateNamespace=true", "ServerSideApply=true"] },
```

Argo CD picks these up because the cp chart sets `application.namespaces: "*"`, and accepts
them because the project's `sourceNamespaces` names the namespace. Since Applications and
CCLComponentSet now share a namespace, two same-named CCLComponentSets can no longer collide on
an Application name; the ownership check remains for a manifest copied in from elsewhere.

plus the `resources-finalizer.argocd.argoproj.io` finalizer, without which deleting an
Application is non-cascading: Argo CD's record disappears and the chart stays installed and
unmanaged on the infra-plane cluster.

`spec` is rebuilt from scratch on every reconcile and compared against the Application as
read; one that reads back unchanged is not written at all. A write is a JSON Patch over
`metadata` and `spec`, plus `operation` on the reconcile that asks Argo CD for a sync, never
an update of the whole object: both Argo CD types model only the fields the operator writes,
so an update would send back an object missing Argo CD's `status` and every spec field not
declared. The patch **adds** rather than replaces, because `operation` does not exist
between syncs and RFC 6902's `replace` requires its target to be there. Replacing `/spec`
still reverts hand edits rather than merging them, and explicit null Helm values survive.
AppProject writes use the same mechanism and never carry an `operation`; its `status` —
where Argo CD keeps JWT token records — is preserved the same way.

That comparison sees only modelled fields, because that is all a read into a partial type
returns. A hand edit that adds nothing but unmodelled spec fields is therefore cleared not
when it is made, but the next time something the operator can see changes the object.
Detecting it directly would need a second, unstructured read of every Application on every
reconcile.

The metadata resource version prevents overwriting concurrent ownership or finalizer
changes. A write rejected because the read behind it was stale is retried in place rather
than failed: neither Argo CD type has a status subresource, so Argo CD's own status writes
move that version, and a cached read can also miss an object that already exists. Losing
either race is routine rather than a fault to report. Ownership uses the same annotation
scheme as above, with
`ccluster.nvidia.com/componentset-{name,namespace}`; the name is also written as a *label*,
which is what the controller selects on to find the Applications it has already released.
An owner reference would be legal here, since both objects share a namespace, and is
deliberately not used: garbage collection would delete every Application at once, and the
whole point of the teardown finalizer is to remove them one at a time in reverse.

### Why not an ApplicationSet, and why not RollingSync

Both were implemented and then removed (commit `2e00925`).

An **ApplicationSet** earns its keep when the set is *derived* from something the operator
does not already know — clusters, Git directories, a matrix. A `CCLComponentSet` enumerates its
components explicitly, so a generator would only interpose a Go-template layer between the
operator and an object it already knows in full, and would force values through a templated
string instead of structured data.

Argo CD's **RollingSync** strategy gates Applications by switching automated sync *off* and,
as of v3.5.1, never switching it back on — which is incompatible with the `selfHeal` that
these Applications depend on for drift correction.

## 5. Ordering: release by withholding

The rollout order is enforced by **not creating** an Application until its predecessors are
converged, rather than by creating them all and holding their syncs. `releaseCount`
(`cclcomponentset_builder.go`):

1. Count the leading run of components that already have an Application — that is what has
   been released. Zero released means release the first: it has nothing to wait on.
2. If *any* released component is not yet `finished` — defined below — release no
   more. Checking all of them, not just the newest, means a component that degrades later
   also halts the rollout, which is what a sequential dependency implies.
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

`Healthy` and `Synced` are Argo CD's verdict on the Application **as Argo CD last compared
it**, which is not the same as its verdict on the spec the operator has just written. On the
first rollout the difference is invisible, because a wave's Application does not exist until
the one before it converges. On every later update it decides the outcome: the operator
rewrites wave 1 in place, re-lists, reads a `Healthy`/`Synced` standing from the *previous*
spec, and releases wave 2 against it.

The operator reuses its Application spec comparison to check the current desired input;
it does not render manifests or reproduce Argo CD's resource diff. For each actual spec
write, it generates a unique request ID and submits the spec, the
`ccluster.nvidia.com/sync-request-id` annotation and a matching `operation.info` entry in
one patch. The parent generation remains diagnostic: operator configuration can change an
Application spec without changing its CCLComponentSet generation. The operation captures
the source submitted with the request, including Helm values.

The current request is recorded independently in `status.componentSyncs`, keyed by
component name and bound to the Application UID. Completion requires both the current
Application spec and the tracked request to match. In particular, a cached Application
cannot prove completion by matching its own old annotation against its own old result.
A deleted and recreated Application cannot reuse the previous object's receipt.

Comparing `status.sync.comparedTo` is deliberately avoided. Argo CD's status merge patch
can remove explicit null Helm values, and the partial API mirror omits unmodelled source
fields. Neither hazard affects the opaque request ID echoed through operation.info.

Request tracking has the following rules:

- Wait for pending or running operations before changing the Application spec or submitting
  another operation. Autosync and self-heal remain enabled throughout.
- Save observed request phases, failures and completion times in ComponentSet status.
  A later self-heal can replace Argo CD's last operation without erasing this receipt or
  causing the operator to repeat a successful sync. Status writes carry the original
  resource version so a concurrent parent edit cannot receive an obsolete completion.
- Recover a request from a direct API read after a crash between the Application write and
  the ComponentSet status write. Repeat an unobserved request only while the Application
  is idle, using the same ID, at fifteen-second intervals and at most five submissions.
  Argo CD separately retries each sync up to five times. Exhaustion is reported as
  `WaveSyncFailed`, rather than restarting retries at watch speed.
- Copy prune and sync options into the requested operation. Argo CD does not inherit them
  from syncPolicy; ServerSideApply is required by this catalog.
- Existing Applications without a request ID continue to use health and sync status, but
  only while their spec matches current desired input. They join tracking on their next
  spec change; unchanged components need no new request when another component changes.

Operation completion and health assessment run in separate Argo CD loops. After a
successful request, require `status.reconciledAt` to be strictly later than its saved
`finishedAt` and any later operation's finish time. Equal second-resolution timestamps
remain pending. Request a hard refresh and requeue while this observation is missing;
only then accept Healthy and Synced. This is a conservative freshness barrier, not an
atomic Argo CD configuration-version acknowledgement. A stronger guarantee would require
Argo CD to bind comparison results to their input version and reject stale status writes.

This request tracking does not serialize updates to already released waves: the existing
reconciliation loop still visits every released component. Sequential update admission is
a separate change. Terminating and pruned components are handled below.

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
| `CCLInfraPlane` | `Available` | The namespace's AppProject exists and the cluster Secret has been mirrored into it. Reasons: `ClusterSecretCreated`, `SecretNotFound`, `ClusterSecretConflict`, `ProjectConflict`. |
| `CCLComponentSet` | `Validated` | Every `infraPlaneRef` resolves to a CCLInfraPlane that is `Available` for its current generation. Reasons: `ComponentsAccepted`, `InfraPlaneNotFound`, `InfraPlaneNotReady`. |
| `CCLComponentSet` | `ComponentsApplied` | The Applications for the released components have been written. Reasons: `ApplicationsCreated`, `ApplicationCRDMissing`, `ApplicationConflict`, `ApplicationFailed`. |
| `CCLComponentSet` | `Progressing` | `True/WaveSyncing` while the rollout is advancing, with a message naming the component being waited on and whether it waits for Argo CD to sync the spec the operator wrote or for what it deployed to become healthy; `True/WaveSyncFailed` when a sync the operator asked for failed, with Argo CD's message; `False/RolloutComplete` when every component has synced the current spec and is healthy. |

`ApplicationCRDMissing` is reported rather than returned as an error: Argo CD may simply not
be installed yet (`make deploy-cp ARGOCD=false`, or the operator started first), and no
amount of retrying fixes it.

Printer columns: `Available` on `CCLInfraPlane`; `Applied` and `Progress` on `CCLComponentSet`.
Each Application also carries `ccluster.nvidia.com/wave`, a 1-based label that is purely
informational — nothing reads it — so that

```bash
kubectl get applications -n <deployment namespace> -L ccluster.nvidia.com/wave
```

shows the order the operator is working through.

### Watches

Neither controller uses `Owns()` — owner references cannot cross namespaces, and for the
same-namespace Applications garbage collection would defeat the ordered teardown. Both use
`Watches` with map functions instead:

- `CCLInfraPlane` watches `Secret`s. A Secret in the Argo CD namespace carrying the
  back-reference annotations maps straight to its CCLInfraPlane; otherwise the controller
  lists CCLInfraPlanes in the Secret's namespace and matches `clusterCredentialsRef`. It
  also watches `AppProject`s: one in the Argo CD namespace maps to every CCLInfraPlane in
  the namespace it is named after, so a project deleted or loosened by hand is repaired.
- `CCLComponentSet` watches `CCLInfraPlane`s (so a cluster becoming `Available` releases the
  CCLComponentSets waiting on it) and `Application`s (so a component becoming healthy releases the
  next one).

There is no `RequeueAfter` polling anywhere except the 15s teardown retry.

## 8. Not built yet

- **`dependsOn`.** Ordering is a straight line; there is no dependency graph and no
  parallelism between independent components. This has a concrete cost now that
  `catalogs/cluster_base` ships a full 17-component order: the legacy Python catalog it was
  taken from gave four components one sync wave and two others another, and those six could
  have installed concurrently. Here they serialise, so installing the catalog is seventeen
  sequential convergence waits.
- **Global values.** The legacy catalog threaded a `Shared` object into every component and
  deep-merged computed per-cluster defaults under the user's values — SPIFFE helper sidecars,
  AWS IRSA environment, Istio CNI paths chosen by cloud provider. None of that is ported.
  `catalogs/cluster_base/final_stack` is the visible casualty: it has no chart-level values
  and its five templates read `.Values.global.cluster.domain`, so it renders against the
  chart's placeholder domain until globals land.
- **No rollout timeout.** A component that never converges blocks the rest indefinitely.
- **One catalog per operator.** `--catalog-repo-url` and `--catalog-revision` are global, so
  a single control plane cannot mix catalogs or revisions.
- **The infra-plane operator is empty.** `internal/controller/ip/` contains only `doc.go`;
  its RBAC role is hand-written with `rules: []`. `CCLClusterConfig` exists but nothing
  reconciles it yet.
- **No cache scoping.** The manager caches all Secrets cluster-wide.
- **Test coverage.** `internal/controller/cp` has substantial envtest suites, but they invoke
  `Reconcile` directly rather than running the controllers in the test manager, so the
  watches and map functions are exercised only as plain functions. `test/e2e` splits into a
  quick tier (`make test-e2e`, the CI default, `ARGOCD=false`) and a full tier
  (`make test-e2e-full`, real Argo CD; in CI only on `release`-labelled PRs). The quick tier covers
  `ApplicationCRDMissing` and, via a CRD-only render trick, the `argoproj.io/applications`
  and `argoproj.io/appprojects` RBAC rules without paying for a real Argo CD install. The full tier goes further: a real catalog
  component reaching `Healthy`/`Synced` under genuine reconciliation (with an explicit check
  that the ~600KB `rollouts.argoproj.io` CRD itself reports `Synced`, not just the aggregate
  Application — the concrete case `ServerSideApply`/`controller.diff.server.side` exist for,
  §6), a two-component deployment proving both `releaseCount` withholding and `teardownNext`'s
  reverse teardown against real convergence timing, and a spec confirming Argo CD's `selfHeal`
  actually reverts drift. `make test-e2e-argocd` never asserted any of this before it was
  retired — it only checked that the argoproj CRDs existed.
