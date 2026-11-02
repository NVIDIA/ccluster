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
back to decide what to do next. The one object the control plane writes into an infra-plane
cluster itself, rather than through Argo CD, is that cluster's `CCLClusterConfig` (§3): the
infra-plane operator's input, which carries the domain the `CCLInfraPlane` was registered
with. It calls no cloud API: each plane's wildcard DNS records are published by external-dns,
from a `DNSEndpoint` the operator writes (§3).

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

The same holds for **external-dns**, an optional dependency of the same chart that publishes
each plane's wildcard records (§3): the operator writes `DNSEndpoint`s and nothing else of
it.

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
| `ccluster-cp-operator` | Runs in the control-plane cluster. Owns both controllers and all real logic. Writes each infra-plane cluster's `CCLClusterConfig` with that cluster's registered credentials, and the `DNSEndpoint` for each plane's wildcard record (§3). |
| `ccluster-ip-operator` | Runs in each infra-plane cluster. Runs every `catalogs/cluster_base` component's Verifier against the `CCLClusterConfig` singleton (§3a). Installs nothing. |
| Argo CD | Control-plane cluster only. Does every install, upgrade and uninstall. |
| external-dns | Control-plane cluster only, optional. Publishes the `DNSEndpoint`s the operator writes into the DNS zones the user created (§3). The `catalogs/cluster_base/external_dns` component still installs one on each infra plane, but that one is no longer needed now that the control plane publishes each plane's records, and it will be removed. |
| `catalogs/` | The Helm chart catalog, in this Git repository. `catalogs/cluster_base/<component>` holds the 17 platform components, `catalogs/deployments/<chart>` the four tenant workload charts, `catalogs/resources/<chart>` the GPU quota queues and tenant volumes they use, and `catalogs/namespace_base` the tenant namespace baseline, each paired with a Go package describing that chart's values (§4). `catalogs/deployments/compute_deployment` also holds the source of the compute-ssh launcher image its pods run. |
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
  domain: ccluster-local.example.com
```

A `CCLInfraPlane` names one Kubernetes cluster that CCluster may deploy to. `clusterCredentialsRef`
points at a `Secret` in the **same namespace** holding that cluster's Argo CD credentials —
the usual `server`, `config`, and optionally `tlsClientConfig` keys. `domain` is the
cluster's DNS domain; it is validated like `CCLClusterConfig`'s and cannot be changed once
set, so a cluster is re-registered rather than renamed. With DNS published,
`*.<name>.<domain>` resolves to each LoadBalancer the plane reports (§3).

`CCLInfraPlaneReconciler` (`internal/controller/cp/cclinfraplane_controller.go`) mirrors that
Secret into the Argo CD namespace (`--argocd-namespace`, by default the operator's own,
where the cp chart installs Argo CD) under the name
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
object the operator owns in the Argo CD namespace — and the last one out deletes it. That is accepted: the
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

### The infra-plane cluster's `CCLClusterConfig`

Once the cluster Secret is mirrored, the controller connects to the infra-plane cluster and
creates or updates its `CCLClusterConfig` singleton (§3a) with `spec.domain`
(`cclinfraplane_clusterconfig.go`). This is the control plane's one direct write to an
infra-plane cluster; everything else goes through Argo CD. It is written directly, not as
an Argo CD Application, so that writing it does not depend on Argo CD, and so that its
status can be read back into the `CCLInfraPlane` straight from the cluster rather than
through argocd-server and polling. The cost is in the credentials it can use, below.

It connects with the **same credentials Secret Argo CD uses**, read the way Argo CD reads it
(`internal/remote`): `server`, and `config`'s `bearerToken`, basic auth or
`tlsClientConfig`. A Secret with no `server`, or `https://kubernetes.default.svc` with no
credentials, registers the operator's own cluster, so the operator uses its own
ServiceAccount — which is why the cp role grants `cclclusterconfigs`. Exec plugins and AWS
IAM (`execProviderConfig`, `awsAuthConfig`) need binaries or a cloud identity only Argo CD's
Pods have, and are refused: a plane registered with `execProviderConfig` or `awsAuthConfig`
works for Argo CD but reports `UnsupportedCredentials` here. Every request is bounded by a
30s timeout, so an unreachable cluster fails a reconcile rather than hanging it, but it
still holds the controller's single worker for up to that long, and every other
CCLInfraPlane waits. Configurable reconcile concurrency is tracked in #116.

The `CCLClusterConfig` is stamped with the same `infraPlaneOwner` annotations as the
mirrored Secret, but without the adopt-if-unclaimed rule: a CCLInfraPlane writes and deletes
only the `CCLClusterConfig` it created, so the check is `claimedBy`, an exact match. Any
other — one applied by hand, or one created for another CCLInfraPlane, whether that plane
registers the same cluster or is gone without its finalizer having reached the cluster — is
left alone and reported as `Conflict` until someone deletes it, after which the plane creates
its own. Unlike the mirror's name, the singleton's is not the operator's choice, and the
cluster is one other people write to. A plane's own `CCLClusterConfig` is written with a
merge patch of its spec and metadata, so it neither fails on the `resourceVersion` the
infra-plane operator's status writes bump nor drops spec fields an older operator does not
know. The finalizer deletes the `CCLClusterConfig` first, if this plane created it, while
the credentials are still registered. A credentials Secret deleted alongside its
CCLInfraPlane, as `ccluster unregister` does, is usually gone by then, so the finalizer falls
back to the mirrored Secret, which it deletes only afterwards. An unreachable cluster is
logged and skipped, because a cluster that is gone must not keep its CCLInfraPlane from
being deleted.

### Watching the infra-plane cluster, and its status coming back

The controller also **watches** each infra-plane cluster's `CCLClusterConfig`, so a change
there — the infra-plane operator writing its status, or someone editing or deleting the
object — reconciles the `CCLInfraPlane` at once. Each reconcile copies the singleton's status,
verbatim, into `status.clusterConfig`. That field reuses the `CCLClusterConfigStatus` type
rather than merging its conditions into the CCLInfraPlane's own: the two sets of conditions
and observedGenerations describe different objects. New fields the infra-plane operator
reports flow through without a control-plane change, such as `loadBalancers`, each of
whose entries gets a wildcard record named after it (§3a, and "DNS" below). The copy is
taken from the live read the write makes, never from the watch's cache, and only when the
write succeeded; otherwise the last copy stays, and
`ClusterConfigApplied` says it is stale.

The watches live in `internal/remote`'s `Registry`: one informer per cluster, from the
generated clientset in `pkg/client` and the same credentials Secret, holding only
`CCLClusterConfig/cluster` (a `metadata.name` field selector). They come and go with the
CCLInfraPlanes, which a controller's own watches cannot, so their events reach the
controller through a single `source.TypedFunc` registered at startup, which hands the
registry the controller's workqueue; an informer handler only ever calls `queue.Add` with
the CCLInfraPlane's key, which never blocks and merges duplicates. Reconciliation reads current state, so an extra
or stale event costs one idempotent pass.

- `Ensure` never blocks a reconcile. It starts the informer in the background and returns;
  a watch that cannot sync within 30s - the cluster is unreachable, refuses the list, or
  lacks the CRD - is logged with the last list error and dropped, and the next reconcile
  starts it again. The watch is only an event source: what the condition reports comes
  from the write, so one unreachable cluster never holds up another plane's watch.
- A watch is replaced when its Secret's UID or resourceVersion changes, and stopped when
  the Secret disappears, when the finalizer runs (before the `CCLClusterConfig` is
  deleted, so that deletion does not reconcile a plane going away), and when a
  CCLInfraPlane is found gone. `Stop` waits for the informer's goroutines to finish.
- Only the elected leader reconciles, so only it holds watches; a new leader rebuilds them
  on its first pass. The registry is a manager Runnable that stops every watch on
  shutdown. A lost connection is retried by the informer and enqueues the plane, whose
  write then reports `ClusterUnreachable`.
- The `ccluster_remote_watches` gauge counts active watches.

Not everything is visible through the watch: the infra-plane operator and its CRD being
installed, or a cluster coming back, arrive as no event on the object. A failed write is
therefore retried every minute, and a written one is reconciled again every
`--infraplane-resync-period` (default 5m) as a safety net.

### DNS: a wildcard record per LoadBalancer, published by external-dns

`*.<name>.<domain>` points at each LoadBalancer the plane reports, `<name>` being its
`ccluster.nvidia.com/loadbalancer` label value (§3a), which the operator copies without
treating any value specially (`cclinfraplane_dns.go`). The catalog's `ingress_nginx` chart
chose `ingress`, so `*.ingress.<domain>` is the cluster's ingress and an Ingress host such
as `oidc-discovery.ingress.<domain>` resolves to it. Naming the record after the label,
rather than claiming all of `*.<domain>` for one LoadBalancer, lets any chart expose
another under its own name with no operator change. The work is split three ways:

| Step | Done by |
| --- | --- |
| Create the DNS zone the domain falls under, and delegate it from its parent | the user, outside ccluster |
| Write `DNSEndpoint` `<namespace>-<name>`: per LoadBalancer with an address, `*.<name>.<domain>`, an A or AAAA record for its IPv4 or IPv6 address, or a CNAME for its hostname, TTL 60 | the operator |
| Publish the record, and the TXT record that marks it as external-dns's, into that zone | external-dns, from the cp chart |

The addresses come from `status.clusterConfig`'s `loadBalancers`, so they arrive through
the remote watch; a plane that cannot be reached keeps its last copy, and its records. A
LoadBalancer without an address has no record; when none has one, the operator deletes
the `DNSEndpoint`, which withdraws them all. A label value may hold characters a DNS label
may not (uppercase, `_`, `.`), and with a long domain the name can pass 253 characters:
such a LoadBalancer gets no record and is reported in `DNSReady`, the rest are published.

- **Which zone.** external-dns picks it, not the operator and not the API: of the public
  zones it manages, the most specific one the record's name falls under. With
  `gpu-a.team-a.example.com`, a zone `team-a.example.com` is shared by the tenant's planes,
  and a zone `gpu-a.team-a.example.com`, if the user creates one, is the plane's own. The
  operator code is the same either way, which is why `CCLInfraPlane` has no zone field.
- **Why external-dns.** The operator stays provider-neutral and calls no cloud API, so it
  holds no cloud credentials. Zones belong with the rest of the user's DNS, typically in
  infrastructure code.
- **Confinement.** The cp chart's external-dns reads only the `crd` source, only in its own
  namespace (`namespaced: true`, a Role), and deletes only records under its own TXT owner
  ID. The `DNSEndpoint`s therefore live in that namespace (`--dns-endpoint-namespace`, the
  operator's own by default), not beside the CCLInfraPlane: anyone who could write a
  `DNSEndpoint` where external-dns reads it could publish any name its credentials reach.
  Like the mirrored Secret, the `DNSEndpoint` carries the `infraPlaneOwner` annotations
  rather than an owner reference, the same `ownedBy` check, and the same `<namespace>-<name>`
  name.
- **Teardown.** The finalizer deletes the `DNSEndpoint`, and external-dns, with
  `policy: sync`, withdraws the record. Zones are the user's and stay.
- **Readiness, and its gap.** external-dns sets a `DNSEndpoint`'s
  `status.observedGeneration` once it has read it, so `Published` means external-dns has
  accepted the record. It also does so for a record that falls under none of its zones,
  which it drops with a debug-level log, so `Published` cannot yet tell that case apart
  ([#174](https://github.com/NVIDIA/ccluster/issues/174)).

The operator writes the `DNSEndpoint` whether or not external-dns is installed. Without its
CRD the write fails, which is reported and retried on the resync rather than every minute,
since it is the steady state of a control plane that publishes no DNS.

### Status

`Available` — `True/ClusterSecretCreated`, or `False` with `SecretNotFound` (the referenced
Secret does not exist yet), `ClusterSecretConflict` or `ProjectConflict`. No failure
requeues: the controller watches Secrets and AppProjects, so the fix re-triggers it.

`ClusterConfigApplied` — `True/Applied`, or `False` with `CRDMissing` (the ip chart is not
installed on the cluster), `Conflict` (a `CCLClusterConfig` this plane did not create, whose
message names the CCLInfraPlane it was created for, if any),
`AccessDenied` (the cluster answered Unauthorized or Forbidden: the credentials are
rejected or lack the rights), `ClusterUnreachable` (any other failure to read or write it,
with the error as the message), `UnsupportedCredentials` or `InvalidCredentials` (`config`
is not JSON); on any of these the copy in `status.clusterConfig` is the last one taken. It
is computed only once `Available` is true, and both are written in one status patch; while
`Available` is `False` it is `Unknown/NotAvailable`, since nothing keeps the
`CCLClusterConfig` in line then. `CRDMissing`, `Conflict`, `AccessDenied` and
`ClusterUnreachable` retry every minute; the credentials reasons wait for the Secret watch.

`DNSReady` — `True/Published` (external-dns has accepted the current `DNSEndpoint`; see
the gap above), or `False` with `LoadBalancerPending` (the plane reports no LoadBalancer
with an address), `Pending` (external-dns has not read the current `DNSEndpoint` yet),
`InvalidLoadBalancerName` (a LoadBalancer's name cannot make a record name; the others'
records are still written), `DNSEndpointCRDMissing` (external-dns is not installed) or
`DNSEndpointConflict` (the name is another plane's), or `Unknown/DNSEndpointError` (writing
or deleting the `DNSEndpoint` failed, with the error as the message). It is written in the
same status patch, a `DNSEndpointError` too, so that it does not hold back the other
conditions or the copy; the error is then returned and retried with backoff.
`DNSEndpointCRDMissing` waits for the resync; the rest wait for a watch.

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
the cluster's domain, which is also its SPIFFE trust domain. For a registered cluster the
control-plane operator creates it from the `CCLInfraPlane`'s `domain` (§3); a cluster run
without a control plane gets one by hand. The operator is idle until it exists.

### Checks live with the catalog, registration with the operator

A cluster_base chart package defines its check beside its `Values`, as a plain exported
`Verify` function. The operator owns everything else: the signature and its contract,

```go
type VerifyFunc func(ctx context.Context, r client.Reader, cfg v1alpha1.CCLClusterConfigSpec) error
```

the registry, how checks run, and how their outcome is reported. The catalog never imports
controller code; `internal/registry/verifier_registry` imports the chart packages and
registers one check per chart in `verifierregistry.Get()`, while the RBAC markers the checks
need stay in `internal/controller/ip`; a chart is registered at most once, so its chart path
identifies its result. Every per-chart registry lives under `internal/registry/`, in a
`*_registry` package whose `Get()` returns its `Registry`, a type embedding the generic
`registry.Registry`, which maps a chart path to a function with `Register`, `Lookup` and
`Charts` (registered charts in registration order). Failing charts are reported in that
order. `TestGet` fails when the registry and `components.ClusterBase` disagree, in content
or order, so no cluster_base chart goes unverified.

Every cluster_base chart starts registered with `alwaysVerified`, which passes without
looking; a chart replaces it with a check of its own when it has something to
look at, as spire_crds does with `common.CRDsEstablished` from `catalogs/common`. Those checks read **live objects
only** — never Argo CD's Application status, never Helm release Secrets. Argo CD renders
and applies without creating Helm releases, so a release-based check would fail in
auto-mode; an Argo CD-based check would fail in self-mode. A check returns `nil` when
the component passes, and otherwise an error whose text says why, which the condition
shows next to the chart; the CCLClusterConfig spec is what it compares the cluster
against.

### The controller

`CCLClusterConfigReconciler` (`internal/controller/ip/cclclusterconfig_controller.go`)
runs every registered Verifier against the spec, concurrently. Each Verifier runs under its own
30-second deadline, and a panic in one is recovered; both count as that component
failing, so one faulty check can neither crash the operator nor hold up the others. The
outcome is one condition, written in the same status patch as the cluster's labelled
LoadBalancers (below):

| Condition | Meaning |
| --- | --- |
| `Verified` | `True/ComponentsVerified` when every cluster_base component passes; `False/ComponentsNotVerified` with a message naming each failing chart and its Verifier's error; `False/LoadBalancerConflict` when they all pass but two LoadBalancer Services carry the same `ccluster.nvidia.com/loadbalancer` value. Conflicts are appended to the message either way. |

A Verifier's error is not a reconcile error: it is the answer, reported rather than retried. The
operator writes nothing but this status, and its ClusterRole grants nothing but the
singleton and its status, and reads of what it inspects; a Verifier that reads a kind adds
a read-only marker for it in the controller package.

The pass also reports LoadBalancers. A chart opts one in by labelling its Service
`ccluster.nvidia.com/loadbalancer: <name>` (`v1alpha1.LabelLoadBalancer`); the operator
lists every Service of type `LoadBalancer` carrying the label and reports each in
`status.loadBalancers`, a list keyed by the label's value, with the first `ip` and the
first `hostname` the Service reports (cloud providers such as AWS address load balancers
by name). An entry whose address is not assigned yet has both empty, so "pending" reads
differently from "absent". A value two Services carry has no right answer: it is left out
of the list and reported in `Verified` instead. The value is the chart's to choose and
becomes the plane's subdomain: the cp operator points `*.<name>.<domain>` at it (§3), so it
has to be a DNS label and unique on the cluster, and neither operator treats any value
specially. The catalog's `ingress_nginx` chart labels its controller Service `ingress`, and
another chart exposes a LoadBalancer the same way, under its own name, with no operator or
API change. The label, not the name, identifies a Service: the name follows the Helm
release, which differs between a `CCLComponentSet` and a `helm install`. Like a Verifier, this reads live Services, never Argo CD or Helm release
state, and an address not assigned yet does not fail `Verified`: it means not reachable
yet, not misconfigured.

Verifiers read state the operator does not write, and the kinds they will read are
installed by the very components being verified — controller-runtime never starts a
controller whose watch source cannot sync — so re-verification is periodic: one minute
while anything fails, five once everything passes. That is one of the two deliberate
exceptions to the "no polling" rule in §7; the other is the control-plane operator's resync
of this singleton (§3). Updates to the singleton trigger a pass only when its generation
changes, so the operator's own status writes do not rerun every Verifier. Services are the
exception to not watching: every API server serves them, so the labelled ones are watched,
without a predicate, and an address the cloud provider assigns reaches
`status.loadBalancers` at once. The manager's cache is not scoped: it holds every Service,
as it holds every object of each kind read, so a Verifier reading a Service sees them all.

Deleting the singleton uninstalls nothing, because the operator installs nothing. The
control plane writes this object's spec and watches it, copying its status into the
`CCLInfraPlane`'s `status.clusterConfig` (§3), which is how an infra-plane's health
surfaces on the control plane.

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
IgnoreDifferences: <the chart's DriftFunc, if registered>(),
```

Argo CD picks these up because the cp chart sets `application.namespaces: "*"`, and accepts
them because the project's `sourceNamespaces` names the namespace. Since Applications and
CCLComponentSet now share a namespace, two same-named CCLComponentSets can no longer collide on
an Application name; the ownership check remains for a manifest copied in from elsewhere.

plus the `resources-finalizer.argocd.argoproj.io` finalizer, without which deleting an
Application is non-cascading: Argo CD's record disappears and the chart stays installed and
unmanaged on the infra-plane cluster.

`IgnoreDifferences` comes from the drift registry, built like the ip operator's Verifiers
(§3a) on the generic `registry.Registry`: `driftregistry.Get()` in
`internal/registry/drift_registry` registers one `DriftFunc` per catalog chart, and
`TestGet` fails unless it covers exactly `components.All`. A chart diffed in full registers
`noDrift`; one with fields that a controller on the infra-plane cluster owns by design
registers a `Drift` function from its own package. istio is the case: istiod patches its
validating webhooks' `failurePolicy` from the rendered `Ignore` to `Fail` once it serves, so
a full diff never reports `Synced` and stalls every later wave. A chart with rules also gets
`RespectIgnoreDifferences=true`; without it every sync, self-heal's included, applies the
rendered value again and the owning controller patches it back. A rule
(`catalogs.DriftSpec`) names a kind and jq paths but no object, since chart values can rename
objects; its jq selects fields by something the chart keeps fixed, such as a webhook's
entry name, so drift elsewhere still shows. Neither callers nor the CRD can
add rules.

Self-heal creates again any rendered object that disappears from the infra-plane cluster,
so a chart must not render an object that deletes itself. `job_deployment` sets no
`ttlSecondsAfterFinished` for this reason: the TTL controller would delete the finished
Job, self-heal would create it again, and it would run again. The finished Job stays until
the CCLComponentSet is deleted, and deleting the set is how a caller removes it. Its pods
stay with it unless the cluster's pod garbage collection deletes them first; a deleted pod
is not created again. Turning self-heal off for that Application would not be enough: automated sync still
syncs an OutOfSync Application when its source or revision changes.

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

### Reading through argocd-server

The Application object records only what the chart rendered and its aggregate health.
Pods and the other objects controllers create from the rendered ones, their live
manifests, and their events, are only in argocd-server's API. `internal/argocd/apiclient`
reads them: an Application's resource tree, the live manifest of one node, and that
node's events. argocd-server answers with Argo CD's own credentials for the destination
cluster, so the cp operator still never connects to an infra-plane itself.

The client authenticates as `ccluster-cp`, an Argo CD account the cp chart declares with
`apiKey` only (no password, no UI login) and one RBAC line, `applications, get, */*`.
That one grant covers the three reads, for every project and namespace. The token is a
bearer credential to that read access. It lives in a Secret in the Argo CD namespace,
which the operator already reads, and is reread on every request, so it can be replaced
in place. It does not expire and is created once. When the cp chart installs Argo CD, a
post-install hook Job of its `argocd-token` dependency creates it, logging in as admin
with `argocd-initial-admin-secret`, the one admin credential a fresh Argo CD always has.
An existing Argo CD's admin credentials are not the chart's to hold, so there the user
declares the account and stores a token in the Secret. Argo CD checks an account token
against its live settings on every request, so neither case restarts Argo CD.

The client mirrors only the parts of the API it uses, like
`internal/argocd/v1alpha1`. It names the Application's namespace and project on every
request, so argocd-server answers a deleted Application with 404, not 403. It tells
apart the few failures a caller can act on by status and message: an unauthorized
token, a deleted Application, a tree not yet cached, and a node gone since the tree was
read.

Argo CD runs in the cp operator's own cluster, so the client reaches argocd-server through
its Service, `--argocd-server-service` in the Argo CD namespace, and does not verify the
server's certificate, which is self-signed unless one is configured. Redirecting that
Service takes about the access needed to read the token's Secret beside it, so
verification would add little; TLS still keeps the token off the wire. The operator
builds the client at startup, without contacting argocd-server, and hands it to the
`CCLComponentSet` reconciler. Nothing reads through it yet: it is the data source for
per-component status (§8).

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
`applicationSpec()` builds, `status.sync.comparedTo` names that source, destination and
ignoreDifferences, and the verdict beside them is `Synced` and `Healthy`. It must also have no pending or
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

- Compare `comparedTo.source`, `comparedTo.destination` and `comparedTo.ignoreDifferences`,
  not `comparedTo` as a whole. `comparedTo.sources` is neither written nor read by the
  operator, which writes exactly one source. Destination and ignoreDifferences are modelled
  in full, so a comparison made before a chart's rules applied does not finish it.
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
  `comparedTo` names the current source, destination and ignoreDifferences and no
  operation is pending or running. Attribute the failure using `operationState.syncResult.source`; when both
  resolved revisions are known, `syncResult.revision` must equal `status.sync.revision`.
  An early error without a sync result falls back to the captured `operation.sync.source`;
  a failure with neither source cannot be attributed. Comparison and operation state
  advance independently, so a new comparison alone does not make an old failure current.
  The operator does not resubmit it; a later successful sync of this source and revision
  resumes the rollout with no CCLComponentSet edit.
- Judge Applications the operator has never written the same way. There is no state to
  join and no first spec change to wait for.
- Rebuild `status.componentSyncs` and the `Progressing` condition on every pass, from one
  list of released Applications, including a pass that fails validation or fails to apply.
  `componentSyncs` has no generation of its own: `status.observedGeneration`, written in
  the same patch, says which spec it describes. A report left over from an earlier pass
  would describe a spec that is no longer desired. Leave out an Application whose
  back-reference names another CCLComponentSet: the operator refuses to write it, so its
  state says nothing about this spec. The `comparedToMatchesSpec` flag indicates whether the latest comparison used the desired
  source, destination and ignoreDifferences. Controller decisions use Applications directly, so a failed
  status write leaves a stale report without blocking rollout recovery.
- Write status with the resource version of the read the report was computed from. A
  conflict means a lagging cache or another writer has moved the CCLComponentSet on. It is
  routine, so the pass ends with a short requeue, not an error. The report is not written
  again in place: it belongs to the generation it was read at, so the next pass recomputes
  it from a fresh read. That version's watch event and the requeue merge into one pass.

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
| `CCLInfraPlane` | `ClusterConfigApplied` | The infra-plane cluster's `CCLClusterConfig` carries the plane's domain (§3). Reasons: `Applied`, `NotAvailable` (status `Unknown`), `CRDMissing`, `Conflict`, `AccessDenied`, `ClusterUnreachable`, `UnsupportedCredentials`, `InvalidCredentials`. |
| `CCLInfraPlane` | `DNSReady` | external-dns has accepted the `DNSEndpoint` pointing `*.<name>.<domain>` at each of the plane's labelled LoadBalancers, and every name makes a valid record (§3). Reasons: `Published`, `Pending`, `LoadBalancerPending`, `InvalidLoadBalancerName`, `DNSEndpointCRDMissing`, `DNSEndpointConflict`, `DNSEndpointError`. |
| `CCLComponentSet` | `Validated` | Every `infraPlaneRef` resolves to a CCLInfraPlane that is `Available` for its current generation. Reasons: `ComponentsAccepted`, `InfraPlaneNotFound`, `InfraPlaneNotReady`. |
| `CCLComponentSet` | `ComponentsApplied` | The Applications for the released components have been written. Reasons: `ApplicationsCreated`, `ApplicationCRDMissing`, `ApplicationConflict`, `ApplicationFailed`. |
| `CCLComponentSet` | `Progressing` | `True/WaveSyncing` while the rollout is advancing, with a message naming the component being waited on and whether it waits for Argo CD to sync the spec the operator wrote or for what it deployed to become healthy; `True/WaveSyncFailed` when the last sync of the current spec failed, with Argo CD's message, until a later sync of it succeeds; `True` with the `Validated` reason, such as `InfraPlaneNotFound`, when validation failed and the component being waited on needs the operator to write its Application; `False/RolloutComplete` when every component has synced the current spec and is healthy. |
| `CCLClusterConfig` | `Verified` | Every `catalogs/cluster_base` component is installed in the infra-plane cluster and configured for it, and no two LoadBalancers share a name (§3a). Reasons: `ComponentsVerified`, `ComponentsNotVerified`, `LoadBalancerConflict`. |

`ApplicationCRDMissing` is reported rather than returned as an error: Argo CD may simply not
be installed yet (`make deploy-cp ARGOCD=false`, or the operator started first), and no
amount of retrying fixes it.

Printer columns: `Domain`, `Available`, `ClusterConfig`, `Verified` (from
`status.clusterConfig`) and `DNS` on `CCLInfraPlane`; `Applied` and `Progress` on
`CCLComponentSet`; `Verified` and `Domain` on `CCLClusterConfig`. No column shows the
LoadBalancers: a printer column shows only the first JSONPath match, and naming one
would give a label value a meaning.
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
  Through a raw source fed by `internal/remote`, it also watches each infra-plane cluster's
  `CCLClusterConfig` (§3), which is also how a new LoadBalancer address reaches the
  plane's wildcard records. With external-dns's CRD installed at startup, it watches
  `DNSEndpoint`s too, mapped back by their annotations, so external-dns accepting
  one, or drift on it, reconciles the plane.
- `CCLComponentSet` watches `CCLInfraPlane`s (so a cluster becoming `Available` releases the
  CCLComponentSets waiting on it) and `Application`s (so a component becoming healthy releases the
  next one).

Control-plane progress is watch-driven: every input to a component's verdict is a field of
an Application, and those arrive as events. Teardown has a 15s retry of its own. The
infra-plane operator periodically re-verifies the live state it does not write, apart from
its labelled LoadBalancer Services, which it watches (§3a), and
the `CCLInfraPlane` controller retries a `CCLClusterConfig` it could not write every
minute, with a slower resync behind its watch (§3).

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
- **Real Verifiers.** Only spire_crds has a check of its own (its CRDs are Established);
  every other cluster_base chart is still registered with `alwaysVerified`, so the
  remaining checks (SPIRE's trust domain against `spec.domain`, workload readiness,
  issuer readiness) are not written yet.
- **Per-component verification results.** The `CCLClusterConfig` status reaches the
  `CCLInfraPlane` (§3), but per-component results are not broken out of the `Verified`
  condition's message yet.
- **No cache scoping.** The manager caches all Secrets cluster-wide, and DNSEndpoints too
  once external-dns is installed.
- **Unpublishable records.** A record that falls under no zone external-dns manages is
  reported `Published` all the same ([#174](https://github.com/NVIDIA/ccluster/issues/174)).
- **Overlapping domains.** Nothing stops two CCLInfraPlanes from registering the same
  domain. Their `DNSEndpoint`s then claim the same record, and external-dns publishes one
  of them.
- **More DNS.** Only labelled LoadBalancers get a record, and only the wildcard under their
  name; a Service not labelled, such as a compute deployment's SSH Service, gets none, and
  nothing answers for `<domain>` itself or for names directly under it. Turning DNS off
  leaves the `DNSEndpoint`s, and so the records, behind.
- **Component status.** Nothing reports a component's state in its own terms yet: a Job
  that succeeded, a Pod in CrashLoopBackOff. The argocd-server client (§4) is the data
  source it will use.
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
