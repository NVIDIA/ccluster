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
| `ccluster-ip-operator` | Runs in each infra-plane cluster. Runs every `catalogs/cluster_base` component's Verifier against the `CCLClusterConfig` singleton (§3a). Installs nothing. |
| Argo CD | Control-plane cluster only. Does every install, upgrade and uninstall. |
| `catalogs/` | The Helm chart catalog, in this Git repository. `catalogs/cluster_base/<component>` holds the 17 platform components, `catalogs/deployments/<chart>` the four tenant workload charts, `catalogs/resources/<chart>` the GPU quota queues and tenant volumes they use, and `catalogs/namespace_base` the tenant namespace baseline, each paired with a Go package describing that chart's values (§4). |
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

## 3a. `CCLClusterConfig` — verifying an infra-plane cluster from the inside

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
cluster's own trust domain, whether external-dns is filtered to the cluster's zone,
whether the ACME issuer ever became Ready. That is what the infra-plane operator answers,
and it answers it the same way whichever installer put the charts there: a
`CCLComponentSet` from the control plane, or a user running `helm install` themselves.

`CCLClusterConfig` is the operator's one input, a cluster-scoped singleton (the CRD's CEL
rule admits only the name `cluster`). Its spec carries just what the checks consume today:
the cluster's domain, which is also its SPIFFE trust domain. It is created once the domain
exists — which is after the cluster has been registered with the control plane — and the
operator is idle until then.

### Verifiers live with the catalog

`catalogs/components` registers the charts in one list per catalog directory, and `All`
concatenates them. Each `ClusterBase` entry carries, beside the chart's `Values`, the
`Verifier` the operator runs for it; the operator is handed `ClusterBase` alone (the
other lists carry `nil`, since nothing verifies them):

```go
type Verifier interface {
    Verify(ctx context.Context, r client.Reader, cfg v1alpha1.CCLClusterConfigSpec) error
}
```

Every cluster_base chart starts with `catalogs.DefaultVerifier`, which passes without
looking; a chart replaces it with a check of its own when it has something to
look at. The contract those checks will follow is fixed now: they read **live objects
only** — never Argo CD's Application status, never Helm release Secrets. Argo CD renders
and applies without creating Helm releases, so a release-based check would fail in
auto-mode; an Argo CD-based check would fail in self-mode. A Verifier returns `nil` when
the component passes, and otherwise an error whose text says why, which the condition
shows next to the chart; the CCLClusterConfig spec is what it compares the cluster
against.

### The controller

`CCLClusterConfigReconciler` (`internal/controller/ip/cclclusterconfig_controller.go`)
runs every Verifier against the spec, concurrently. Each Verifier runs under its own
30-second deadline, and a panic in one is recovered; both count as that component
failing, so one faulty check can neither crash the operator nor hold up the others. The
outcome is one condition:

| Condition | Meaning |
| --- | --- |
| `Verified` | `True/ComponentsVerified` when every cluster_base component passes; `False/ComponentsNotVerified` with a message naming each failing chart and its Verifier's error. |

A Verifier's error is not a reconcile error: it is the answer, reported rather than retried. The
operator writes nothing but this status, and its ClusterRole grants nothing but the
singleton and its status; a Verifier that reads a kind adds a read-only marker for it in
the controller package.

Only the singleton is watched. Verifiers read state the operator does not write, and
the kinds they will read are installed by the very components being verified —
controller-runtime never starts a controller whose watch source cannot sync — so
re-verification is periodic: one minute while anything fails, five once everything
passes. That is the one deliberate exception to the "no polling" rule in §7. Updates to
the singleton trigger a pass only when its generation changes, so the operator's own
status writes do not rerun every Verifier.

Deleting the singleton uninstalls nothing, because the operator installs nothing. The
control plane does not read this status yet; it holds the credentials to, and doing so is
how an infra-plane's health would surface on its `CCLInfraPlane` later.

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
- **`values` preserves the component's Helm configuration.** It is `apiextensionsv1.JSON`
  with `x-kubernetes-preserve-unknown-fields`, serialized as canonical YAML in Argo CD's
  `helm.values`. This preserves explicit nulls in the recorded comparison. The operator
  does not validate values against the chart.

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
          Path: component.Chart, Helm: helmValues(component.Values) },
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
`metadata` and `spec`, never an update of the whole object: the partial Argo CD types omit
fields from both spec and status, so an update would discard them. `operation` is not among
the paths written — it is Argo CD's, and a write of the operator's own over one Argo CD had
not started would lose that one. Replacing `/spec` still reverts hand edits rather than
merging them, and explicit null Helm values survive. AppProject writes use the same
mechanism; its `status` — where Argo CD keeps JWT token records — is preserved the same way.

`ApplicationSource` and `ApplicationSourceHelm` retain non-zero unmodelled fields on read.
Source drift therefore changes the comparison and is removed by the next spec write.
Unmodelled fields outside source, including extra syncPolicy fields and AppProject spec
fields, are only removed when another visible change causes a write.

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
components explicitly, so a generator would only add a Go-template layer to an object the
operator can construct directly. Helm values are serialized to YAML without template
interpolation.

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

The operator settles this by reading what Argo CD compared, not by tracking the syncs it
asks for. `status.sync.status`, `status.sync.comparedTo` and `status.health.status` are
written from the output of one comparison, in consecutive statements of one Argo CD
reconcile. A component is finished when its Application's `spec` is the one
`applicationSpec()` builds, `status.sync.comparedTo` names that source and destination,
and the verdict beside them is `Synced` and `Healthy`. It must also have no pending or
running operation and no terminal failure attributed to that source and resolved revision.
Argo CD excludes hooks from aggregate health and sync, so a `Healthy`/`Synced` verdict
can coexist with a running or failed PostSync hook. There is no sync receipt to catch and
therefore no window in which catching it can be missed: a verdict that does not describe
the current spec simply does not match, whoever ran the sync behind it and whether or not
the operator was watching at the time.

This requires Helm values to be written as `source.helm.values`, a YAML string, and not as
`source.helm.valuesObject`. Argo CD renders the two identically - `ValuesYAML()` converts
the object form with `sigs.k8s.io/yaml` and uses the string form as it stands, so the
operator writes exactly that conversion - but it writes its own status with a merge patch,
and a merge patch deletes the keys of an object whose value is null. A `valuesObject`
carrying one is recorded in `comparedTo` without it, and the comparison never matches the
spec that produced it. A string is one scalar and is replaced whole.

Comparison has the following rules:

- Compare `comparedTo.source` and `comparedTo.destination`, not `comparedTo` as a whole.
  `comparedTo.ignoreDifferences` and `comparedTo.sources` are neither written nor read by
  the operator, which writes exactly one source and no ignoreDifferences. Destination is
  modelled in full.
- Retain unmodelled source keys on read so a field such as `helm.valueFiles` affects both
  spec reconciliation and completion. Replacing `/spec` clears source drift. Argo CD's
  status patch then removes those keys from `comparedTo` by diffing old and new status.
- Ignore zero-valued unmodelled keys and normalize an empty `helm` block to nil. This
  matches Argo CD's omitted optional fields and prevents future zero-valued CRD defaults
  from causing repeated spec writes. Empty `kustomize` and `plugin` objects are retained
  because they select the source type.
- Serialize values canonically so key order, whitespace and number formatting do not
  cause another spec write or prevent completion.
- Ask Argo CD for no sync at all. An Application whose `spec.source` differs from
  `status.sync.comparedTo.source` is queued for comparison at once, and automated sync runs
  the sync that follows, so a request of the operator's own would only duplicate it. The
  spec write therefore never waits for an operation, and one busy component no longer holds
  back the specs of the components after it.
- Wait for a pending or running operation before finishing a component. Hook resources are
  excluded from aggregate health and sync, so `Healthy`/`Synced` can precede the completion
  of a PostSync hook.
- Report a terminal `Failed` or `Error` operation as `WaveSyncFailed`, but only once
  `comparedTo` names the current source and destination and no operation is pending or
  running. Attribute the failure using `operationState.syncResult.source`; when both
  resolved revisions are known, `syncResult.revision` must equal `status.sync.revision`.
  An early error without a sync result falls back to the captured `operation.sync.source`;
  a failure with neither source cannot be attributed. Comparison and operation state
  advance independently, so a new comparison alone does not make an old failure current.
  The operator does not resubmit it; a later successful sync of this source and revision
  resumes the rollout with no CCLComponentSet edit.
- Judge Applications the operator has never written the same way. There is no state to
  join and no first spec change to wait for.
- Rebuild `status.componentSyncs` from the released Applications each pass. The
  `comparedToMatchesSpec` flag indicates whether the latest comparison used the desired
  source and destination. Controller decisions use Applications directly, so a failed
  status write leaves a stale report without blocking rollout recovery.

Every pass writes the spec of every released component, and no component's operation holds
back another's write, so a CCLComponentSet update reaches all released waves in the same
pass. Release follows list position; updates to released waves do not. Sequential update
admission is a separate change.

`comparedTo.source` records a revision string such as `main`, not its Git commit SHA.
A catalog commit behind an unchanged revision leaves `applicationSpec()` unchanged, so the
operator writes nothing; Argo CD autosync handles that repository change.

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
| `CCLComponentSet` | `Progressing` | `True/WaveSyncing` while the rollout is advancing, with a message naming the component being waited on and whether it waits for Argo CD to sync the spec the operator wrote or for what it deployed to become healthy; `True/WaveSyncFailed` when the last sync of the current spec failed, with Argo CD's message, until a later sync of it succeeds; `False/RolloutComplete` when every component has synced the current spec and is healthy. |
| `CCLClusterConfig` | `Verified` | Every `catalogs/cluster_base` component is installed in the infra-plane cluster and configured for it (§3a). Reasons: `ComponentsVerified`, `ComponentsNotVerified`. |

`ApplicationCRDMissing` is reported rather than returned as an error: Argo CD may simply not
be installed yet (`make deploy-cp ARGOCD=false`, or the operator started first), and no
amount of retrying fixes it.

Printer columns: `Available` on `CCLInfraPlane`; `Applied` and `Progress` on `CCLComponentSet`;
`Verified` and `Domain` on `CCLClusterConfig`.
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

Control-plane progress is watch-driven: every input to a component's verdict is a field of
an Application, and those arrive as events. Teardown has a 15s retry of its own. The
infra-plane operator periodically re-verifies the live state it does not write (§3a).

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
- **Real Verifiers.** Every cluster_base chart still carries `catalogs.DefaultVerifier`;
  the checks themselves (SPIRE's trust domain against `spec.domain`, workload readiness,
  issuer readiness) are not written yet.
- **The control plane does not read `CCLClusterConfig`.** An infra-plane's verification
  result stays in that cluster; surfacing it on the `CCLInfraPlane` would need the cp to
  read through the registered credentials. Per-component results are also not broken out
  of the `Verified` condition's message yet.
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
