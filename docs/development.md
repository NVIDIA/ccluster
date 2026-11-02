# Development mechanics

Start with the [README local loop](../README.md#local-development) and
[agent guide](../AGENTS.md). Read [DESIGN.md](../DESIGN.md) before changing controller
behaviour. Test selection is in [Testing](testing.md); image and CI contracts are in
[Build and release](build-and-release.md).

## Adding a controller or API

The two-binary layout requires manual wiring. There is no `cmd/main.go`,
`config/default/`, `config/manager/` or top-level `config/rbac/`.

1. Scaffold with kubebuilder, then move the reconciler into `internal/controller/cp/`
   or `internal/controller/ip/` and fix its package name. API types belong in
   `api/ccluster/<version>/`, whatever directory kubebuilder scaffolds them into.
2. Add its `SetupWithManager` call to `setupControllers` in the corresponding
   `cmd/ccluster-*-operator/main.go`. Preserve the cp binary's kubebuilder scaffold markers.
3. Move any scaffolded `config/samples/` CR into `examples/<scenario>/` and remove the
   resurrected samples directory. Examples are not kustomize inputs.
4. Put RBAC markers in the controller's own operator package and regenerate:

```bash
make manifests generate
make client-generate
make helm-generate
make python-generate
```

Godoc on API types becomes CRD descriptions and Python schema documentation, so changes to
comments require generation too. In a host-manager loop, separately update the target
cluster's CRDs with `make install`; Helm installations receive them through chart deployment.
An old CRD schema can prune newly added fields on apply.

Export every condition type and reason a controller reports from the API package, beside
the status type it belongs to, never as private constants in the controller. Name them
after their kind, `<Kind>Condition<Type>` and `<Kind>Reason<Reason>`, such as
`CCLComponentSetConditionProgressing` and `CCLInfraPlaneReasonClusterSecretCreated`.
Clients match on these values, so changing one is a breaking API change.

Keep leader-election IDs `cp.ccluster.nvidia.com` and `ip.ccluster.nvidia.com` distinct.

## Per-operator RBAC

`make manifests` generates CRDs over `./api/...` into `config/crd/bases/` (through
`make crd-generate`), and one RBAC role per operator: `./internal/controller/cp/...` into
`config/cp/rbac/role.yaml` and `./internal/controller/ip/...` into
`config/ip/rbac/role.yaml`. Markers only grant rights to the operator whose package
contains them. The overlays prefix the shared `manager-role` name for each operator.

Each deploy overlay includes `config/crd` and deletes the CRDs the other operator serves
with a `$patch: delete` patch, because kustomize refuses to load a file from outside a
kustomization's directory, so a per-operator CRD list cannot live under `config/crd/`.
The cp chart ships `CCLInfraPlane` and `CCLComponentSet`; the ip chart ships
`CCLClusterConfig`; `make install` installs all three.

## Helm generation

`config/base/` and `config/{cp,ip}/` are the source for operator deployments and RBAC.
`make helm-generate` builds `dist/install-{cp,ip}.yaml` and runs the pinned kubebuilder
Helm plugin to generate `charts/{cp,ip}/chart/`. The install YAML is generator input;
deploy through Helm.

- `Chart.yaml` and `values.yaml` are preserved. They hold dependencies and the pinned
  `fullnameOverride` values `ccluster-cp` / `ccluster-ip`, which tests and recipes use.
- `charts/ip/chart/values.schema.json` is hand-written and preserved too. It rejects
  `rbac.namespaced: true`: the plugin renders every role as a namespaced `Role` under
  that value, and a `Role` cannot grant access to the cluster-scoped `CCLClusterConfig`.
- `templates/` is deleted and rebuilt on every run, including helpers and notes.
  Additional hand-written resources belong in a separate chart. The chart `.helmignore`
  is generated too.
- `PROJECT` records only the last chart. The Makefile explicitly generates both and runs
  cp last; a bare `kubebuilder edit` only regenerates cp.
- Remove plugin-injected `helm-deploy`, `helm-uninstall` and `install-helm` targets;
  the repository supplies its own per-operator deployment targets.

To upgrade the generator, change `KUBEBUILDER_VERSION` in the Makefile, regenerate, and
compare the preserved values files against a fresh chart. A new template can require a
renamed or new values key without the plugin updating `values.yaml`. Check rendering with
`make helm-lint`; regeneration alone will not expose every mismatch.

The cp chart's Argo CD dependency is pinned in `Chart.lock`. Use
`make helm-deps-update-cp` to resolve a dependency change; never edit the lock by hand.
Keep `ServerSideApply=true` on Applications and `controller.diff.server.side` enabled
in Argo CD; [DESIGN.md](../DESIGN.md#6-why-serversideapply-is-mandatory) explains the pair.

## Catalog components

Each chart directory under `catalogs/` is both a Helm chart and a Go package covered by
module-wide builds, tests and lint. A compile error here also breaks controller-gen.
See [the catalog design](../DESIGN.md#the-typed-catalog-and-what-it-does-not-guarantee)
for validation boundaries and why deployment choices belong to callers.

There are three catalogs and one standalone chart. `catalogs/cluster_base/` wraps
upstream platform charts, so each `Values` nests the upstream values under the chart's
key. `catalogs/deployments/` holds the tenant workload charts written here
(`inference_deployment`, `compute_deployment`, `job_deployment`, `dynamo_deployment`);
their `Values` is the chart's own top-level document with camelCase keys, the pod-level
fields most of them share come from embedding `deployments.Workload`, the autoscaling
and rollout types live in the `deployments` package, and every `Values` implements
`Validate()`. `catalogs/resources/` holds the resources those workloads reference:
`kai_gpu_queues` (the KAI scheduler quota tree the `kaiQueue` value joins) and
`persistent_volume` (a claim the `volumeMounts` value mounts). `catalogs/namespace_base/`
is the per-tenant namespace baseline the workloads assume is installed first; it takes
no values. These charts derive everything namespace-scoped from
the release namespace, which Argo CD sets to the component's `namespace`; they take no
tenant identifier. Render one locally with
`helm template <name> catalogs/<path> -n <namespace> -f <values>`.

When adding or changing a component:

- Keep the chart directory snake_case; it is the path Argo CD resolves. The Go package
  drops underscores, for example `gpu_operator` becomes `gpuoperator`, with named imports.
- Export `Chart`, `Values`, and `New(namespace string, values Values)
  (v1alpha1.ComponentEntry, error)`. `New` delegates to `catalogs.Entry`; do not mirror
  the API types or add a separate required-values parameter.
- Optional scalars are pointers with `omitempty`, so explicit `false` survives marshaling.
  Required fields are non-pointer without `omitempty`. Add `Values.Validate()` when needed;
  `catalogs.Entry` calls it. Hand-written CRs bypass this validation.
- Register the component in `catalogs/components/components.go`, in the list for its
  directory (`NamespaceBase`, `ClusterBase`, `Deployments` or `Resources`); `All`
  concatenates them, `hack/pyschema` and the tests read it, and `TestAllCoversTheTree`
  fails if `All` and the `catalogs/` tree disagree. The chart's path under `catalogs/` becomes
  its path under `ccluster.catalogs` in Python automatically, whether it sits in a
  catalog subdirectory or directly under `catalogs/`. A `cluster_base` component also goes into
  `catalogs/cluster_base/cluster_base.go`, which supplies a sample order and deployment
  choices, not a universally deployable default.
- A `cluster_base` component is also registered with the ip operator's verifier registry,
  `verifierregistry.Get()` in `internal/registry/verifier_registry`, which must cover exactly
  `components.ClusterBase`, in its order (`TestGet` fails otherwise). Start with `alwaysVerified`.
  A check of the chart's own is an exported `Verify` function beside `New` in the chart's
  `component.go` with the `verifierregistry.VerifyFunc` signature (see `spire_crds`; the chart package
  never imports controller code), reads live objects only (never Argo CD or Helm state),
  and reading a kind needs a `+kubebuilder:rbac` marker in `internal/controller/ip`, the
  kind's scheme in `cmd/ccluster-ip-operator`, and `make helm-generate`.
- Every component is also registered with the cp operator's drift registry,
  `driftregistry.Get()` in `internal/registry/drift_registry`, which must cover exactly
  `components.All` (`TestGet` fails otherwise). Start with `noDrift`. A field that a
  controller on the infra-plane cluster rewrites by design, such as istiod's webhook
  `failurePolicy`, keeps the Application `OutOfSync` and stalls the rollout; export a
  `Drift` function beside `New` with the `driftregistry.DriftFunc` signature, returning
  `catalogs.DriftSpec` rules (see `istio`). A rule names a kind and jq paths, not objects:
  narrow it with a jq `select` on something the chart keeps stable whatever its values,
  such as a webhook's entry name.
- Do not render objects that delete themselves, such as a Job with
  `ttlSecondsAfterFinished`: self-heal creates them again. See
  [the generated Application](../DESIGN.md#the-generated-application).
- A chart whose required values have no default carries `ci/<case>-values.yaml`, the
  required values only; a chart with exclusive modes carries one file per mode (see
  `persistent_volume`). `make helm-lint-catalogs`, run by CI, renders the chart with
  each file, or with its defaults when there is none.
- Include a hand-written `.helmignore` excluding `*.go`, so source does not enter chart
  archives. List any image source subdirectory too. This is separate from the generated
  operator-chart ignore files.
- Run `make python-generate` after changing values types or their doc comments.

### Images built beside a chart

When a catalog chart runs a binary built in this repository, the binary's source lives
in a subdirectory of that chart. The chart's templates and the binary share a contract:
paths inside the image, subcommands and environment variables. Keeping both in one
directory puts a contract change in one diff. `compute_deployment/compute-ssh/` is the
only such image today. Operator binaries stay in `cmd/`.

- The subdirectory is `package main`. It is not a catalog component: it has no
  `Chart.yaml`, no entry in `components.All` and no Python model.
  `TestAllCoversTheTree` skips directories without a `Chart.yaml`.
- The binary does not import the chart's package. That package imports
  `api/ccluster/v1alpha1` through `catalogs.Entry`, which would link API machinery into
  the image.
- The chart's `.helmignore` lists the subdirectory, so nothing in it reaches `.Files`
  or a chart archive.
- The shared Dockerfile builds the image, and it is published with the operator images.
  The chart's default tag is the root `VERSION`; see
  [Build and release contracts](build-and-release.md).
- Between releases, the chart's default image is the launcher at `VERSION`, while a dev
  chart renders the catalog at its own commit. A change to the contract must keep the
  chart working with that launcher until the next release. To try an unreleased
  launcher, set the chart's image value (`sshInjectorImage` for `compute_deployment`) to
  the `:<sha>` image `main.yml` pushed for that commit.

## Python generation

`make python-generate` builds the `ccluster-py` package in two steps. `hack/pyschema`, a Go
program, writes JSON Schema for everything the package is generated from, plus one small
Python file per API version; `datamodel-code-generator` turns each schema into a Pydantic
module. Nothing in `hack/pyschema` lists kinds or charts, so adding a CRD or a chart needs
no change to it.

What pyschema writes:

- **CRD kinds.** For every CRD under `config/crd/bases` and every version it serves, one
  schema per kind: the `spec` schema at the document root, titled `<Kind>Spec`, and the
  `status` schema under `definitions`, titled `<Kind>Status`. Only spec and status are
  generated, because they are the parts that differ between kinds; the schemas are
  controller-gen's, so their constraints and docstrings reach Pydantic. Nested class names
  come from `+kubebuilder:title` markers on the Go types (`ComponentSpec`,
  `LocalObjectReference`), which controller-gen writes into the CRD and the generator uses.
- **The version package.** `python/src/ccluster/<version>/__init__.py`: the `Kind` enum and
  one concrete `CustomResource[<Kind>Spec, <Kind>Status]` per kind, with `kind` narrowed to
  its name. This specialises the hand-written template in `<version>/base.py`, which holds
  `API_VERSION`, `ObjectMeta` (name, namespace, labels, annotations; server-managed fields
  are kept on read) and the generic `CustomResource` with the fixed `apiVersion`. The
  envelope is the same for every Kubernetes object, so it is written once.
- **Charts.** For every chart in `catalogs/components.All`, one schema reflected from the
  chart's Go `Values` with `invopop/jsonschema`, Go doc comments included.

`datamodel-code-generator` then writes `<version>/<kind>.py` for a CRD and
`<chart path>/__init__.py` for a chart; the Makefile appends the `CHART` constant to the
latter. Catalog directories (`cluster_base`, `deployments`, `resources`) carry no
`__init__.py` and are namespace packages.

Generated, deleted and rebuilt on each run:

- `python/src/ccluster/<version>/<kind>.py` and `<version>/__init__.py` (currently `v1alpha1`)
- `python/src/ccluster/catalogs/<catalog>/<chart>/` and `python/src/ccluster/catalogs/<chart>/`

Hand-written: `python/src/ccluster/__init__.py`, `python/src/ccluster/client.py`,
`python/src/ccluster/cli.py`,
`python/src/ccluster/<version>/base.py`, `python/src/ccluster/catalogs/__init__.py`,
`python/pyproject.toml`, `python/README.md` and `python/tests/`. Generator changes belong in
`hack/pyschema/` or the Makefile recipe, never in emitted modules. Optional fields default to
`None`; consumers dump with `mode="json", by_alias=True, exclude_none=True` to keep an
explicit `False` while omitting unset values. See [Python usage](../python/README.md). CI
regenerates and fails on any drift.

## Go client generation

`make client-generate` runs applyconfiguration-gen, client-gen, lister-gen and
informer-gen from `k8s.io/code-generator`, at the version of `k8s.io/client-go` in
`go.mod`, over the API packages. They write apply configurations, a typed clientset with
a fake, listers and a shared informer factory into `dist/_client/`, which replaces
`pkg/client/` only once all four succeed. A failed run leaves the last client in place:
`internal/remote` imports it, so without it `make manifests` cannot load the cp operator.
The apply configurations give the clientset its `Apply` methods and the fake its
field-managed `NewClientset`; without them the fake offers only the deprecated
`NewSimpleClientset`.

The field-managed fake types every write with an OpenAPI schema that applyconfiguration-gen
embeds in `pkg/client/applyconfiguration/internal/`; a kind missing from it fails every
create, update and apply. `hack/clientschema` builds that schema from the CRDs with the
API server's own CRD OpenAPI builder, so `client-generate` runs `crd-generate` first. It
does not run `manifests`, whose RBAC step loads the cp operator and so needs `pkg/client/`
to exist: `make client-generate` can always rebuild a missing client. The schema also gives
each kind an `Extract` function, which reads back the fields one manager owns. A kind is
included when its type carries `+genclient`; a cluster-scoped kind such as
`CCLClusterConfig` also needs `+genclient:nonNamespaced`. A new top-level kind needs these
markers too.

The generators take a package's group and version from its last two directories, so the
API lives in `api/ccluster/v1alpha1/` and the generated packages sit under
`ccluster/v1alpha1`. They read `+groupName` only from a file named `doc.go`, so the
package markers live in `api/ccluster/v1alpha1/doc.go`; without it they would address
the group as `ccluster` rather than `ccluster.nvidia.com`. The generated code also
expects `SchemeGroupVersion` and `Resource` beside kubebuilder's `GroupVersion`, in
`groupversion_info.go`. `test/client/` runs the generated client against envtest, which
proves those markers, that glue and the schema rather than the generated code. CI
regenerates and fails on any drift.

## License headers

Every hand-written source file starts with the two SPDX lines, in the file's comment
syntax:

```
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
```

Go uses a `/* */` block before the package clause, Python, shell, YAML, TOML, Makefile
and Dockerfile use `#` lines (after any shebang), and catalog Helm templates use a
`{{- /* */ -}}` comment so nothing reaches the rendered output. Generated Go and Python
get the header from `hack/boilerplate.go.txt` and `hack/boilerplate.py.txt`; generated
CRDs, cp RBAC and operator chart templates carry none and are excluded from the check.
`make check-headers` runs `hack/check-headers.sh` over every tracked file and is a
required check on pull requests.

## Cluster operations

Use the [README](../README.md#local-development) for initial setup. `make init`, `build`,
`load` and `deploy` drive both operators; their `-cp` / `-ip` halves target one operator.
`build-cp` and `build-ip` build binaries; `docker-build-cp` and `docker-build-ip` build images.
`make help` lists the remaining targets.

Pin a dedicated kubeconfig before touching a cluster. For example, after creating the
control-plane development cluster:

```bash
kind get kubeconfig --name ccluster-cp > ~/.kube/ccluster-dev.yaml
export KUBECONFIG=~/.kube/ccluster-dev.yaml
```

`make deploy` explicitly selects the two development contexts. Standalone `deploy-cp` and
`deploy-ip` use the current context unless `KUBE_CONTEXT_CP` / `KUBE_CONTEXT_IP` is set;
these become Helm's `--kube-context`. Other cluster commands need the pinned kubeconfig.
`KUBECTL` is quoted as an executable, so `KUBECTL="kubectl --context foo"` does not work.

Development clusters are `KIND_CLUSTER_CP=ccluster-cp` and `KIND_CLUSTER_IP=ccluster-ip`.
Tests use their own pair, `KIND_CLUSTER_E2E_CP=ccluster-test-e2e-cp` and
`KIND_CLUSTER_E2E_IP=ccluster-test-e2e-ip`, and delete both on completion. Never point e2e
at a development or real cluster. `make clean` deletes both development clusters;
`make clean-all` also deletes `bin/` and `dist/`, requiring tools to be downloaded again.

### CRD ownership and Argo CD

`make deploy-cp` installs CRDs through Helm. Do not precede it with `make install`:
Helm refuses existing CRDs without its ownership labels and annotations. To adopt them
without deleting CRs, run `make helm-adopt-crds-cp`, then deploy. `make install` is for
the host-manager loop with `make run-cp`, where no chart is installed.

`make deploy-cp ARGOCD=false` skips the dependency when Argo CD already exists or is
intentionally absent. The operator looks for Argo CD in its own namespace unless
`--argocd-namespace` is set: pass an existing Argo CD's namespace as `ARGOCD_NAMESPACE`.
`make run-cp` has no namespace of its own and uses `ARGOCD_NAMESPACE`, or `argocd`. The operator must tolerate the Application CRD being absent at
startup. Argo CD clones `CATALOG_REPO_URL` itself; a private catalog requires credentials,
provided through `GITHUB_TOKEN` to `make deploy-cp`. Missing credentials surface as
Application comparison errors. Use `CATALOG_REVISION` to select the catalog commit or tag.

### Infra-plane access

The cp operator writes and watches each infra-plane cluster's `CCLClusterConfig`
(DESIGN.md §3) using the CCLInfraPlane's credentials Secret, the same one Argo CD uses, and
copies its status into the CCLInfraPlane's `status.clusterConfig`. In the local two-cluster
setup that Secret must name an address reachable from the cp operator's Pod, as for Argo CD.
A plane registering the cp cluster itself (`server: https://kubernetes.default.svc`, no
token) is written with the operator's own ServiceAccount and reports
`ClusterConfigApplied=False/CRDMissing` until the ip chart's CRD is installed there.
Watch starts, stops and failures are logged under the `remote` logger, and the
`ccluster_remote_watches` metric counts active watches. `--infraplane-resync-period`
(default 5m) is a safety net behind the watch; failed writes are retried every minute.

### DNS and external-dns

The operator always writes a DNSEndpoint per CCLInfraPlane; without
external-dns nothing publishes it (`DNSReady=False/DNSEndpointCRDMissing`). To publish
`*.<name>.<domain>` for each CCLInfraPlane, pointing at its LoadBalancers labelled
`ccluster.nvidia.com/loadbalancer: <name>` (`*.ingress.<domain>` at its ingress; DESIGN.md §3):

1. Create the DNS zones the planes' domains fall under, and delegate them from their
   parents. One zone can serve several planes (`team-a.example.com` for
   `gpu-a.team-a.example.com` and `gpu-b.team-a.example.com`), or a plane can have its own
   (`gpu-a.team-a.example.com`); external-dns writes each record into the most specific
   zone it manages.
2. Configure the cp chart's external-dns for your provider. For Route53 with IRSA:

   ```yaml
   # dns-values.yaml
   external-dns:
     provider:
       name: aws
     domainFilters:
       - team-a.example.com
     serviceAccount:
       annotations:
         eks.amazonaws.com/role-arn: arn:aws:iam::<account>:role/<external-dns role>
   ```

3. Deploy with external-dns, which `DNS=true` turns on (`external-dns.enabled=true`):

   ```bash
   make deploy-cp KUBE_CONTEXT_CP=<context> DNS=true HELM_ARGS='-f dns-values.yaml'
   ```

The operator writes the DNSEndpoints into its own namespace, which the chart's external-dns
reads. `--dns-endpoint-namespace` moves them for an external-dns installed elsewhere, which
should then read only that namespace; `make run-cp`, which has no namespace of its own,
passes the cp chart's (`HELM_NAMESPACE_CP`).
external-dns's Route53 policy, scoped by name with `team-a.example.com` standing for your
zones:

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Action": ["route53:ListHostedZones", "route53:ListTagsForResource", "route53:ListTagsForResources"],
      "Resource": "*"
    },
    {
      "Effect": "Allow",
      "Action": "route53:ListResourceRecordSets",
      "Resource": "arn:aws:route53:::hostedzone/*"
    },
    {
      "Effect": "Allow",
      "Action": "route53:ChangeResourceRecordSets",
      "Resource": "arn:aws:route53:::hostedzone/*",
      "Condition": {
        "ForAllValues:StringLike": {
          "route53:ChangeResourceRecordSetsNormalizedRecordNames": ["*.team-a.example.com"]
        }
      }
    }
  ]
}
```

`DNSReady=True/Published` means external-dns read the current DNSEndpoint, not that it
wrote the records: it marks the DNSEndpoint read before it calls the provider, and a record
it skips or fails to write shows only in its logs
([#174](https://github.com/NVIDIA/ccluster/issues/174)). If the name does not resolve,
check external-dns's logs for provider errors such as denied IAM calls, at
`--log-level=debug` for `Skipping record`, and its `--domain-filter`. One external-dns writes every plane's record with one set of
credentials; separate credentials per tenant need an external-dns per tenant, each reading
only its tenant's DNSEndpoints, which the chart does not set up.

### Argo CD API token

The operator reads Applications' resource trees, live manifests and events from
argocd-server's API as `ccluster-cp`, a read-only Argo CD account. It authenticates with a
token stored under the key `token` of Secret `ccluster-cp-argocd-token` (`--argocd-token-secret`)
in the Argo CD namespace (`--argocd-namespace`). The token does not expire and is created
once. The operator reaches argocd-server through its Service in the same namespace,
`--argocd-server-service`, without verifying its certificate, so Argo CD must run in the
operator's cluster. The chart pins the bundled Argo CD's names with
`argo-cd.fullnameOverride`, so its Service is always `ccluster-cp-argocd-server`, which the
chart's default `manager.args` and `make deploy-cp` both pass.

With the bundled Argo CD, `make deploy-cp` (any `helm install` of the cp chart) creates it.
The chart declares the account and its RBAC line (`argo-cd.configs` in
`charts/cp/chart/values.yaml`), and the `argocd-token` dependency
(`charts/cp/argocd-token/`) runs a post-install and post-upgrade hook Job. The Job logs in
as admin with `argocd-initial-admin-secret`, generates the token and stores it in the
Secret. When the Secret already holds a token it does nothing, so deleting
`argocd-initial-admin-secret` after installation is safe. To replace the token, remove the
key and upgrade:

```bash
kubectl -n ccluster-cp-system patch secret ccluster-cp-argocd-token --type=json \
  -p '[{"op":"remove","path":"/data/token"}]'
make deploy-cp KUBE_CONTEXT_CP=kind-ccluster-cp
```

Removing the key does not revoke the old token. Argo CD keeps accepting it until it is
deleted: list the account's token IDs with `argocd account get --account ccluster-cp` and
revoke with `argocd account delete-token --account ccluster-cp <id>`.

With an existing Argo CD in the same cluster (`ARGOCD=false`), create the token once with an Argo CD login that
may update the account, such as admin or an SSO user with `role:admin`:

1. Declare the account in that Argo CD's `argocd-cm`: `accounts.ccluster-cp: apiKey`.
2. Grant it read access in `argocd-rbac-cm`, for example as its own key
   `policy.ccluster-cp.csv`: `p, ccluster-cp, applications, get, */*, allow`.
3. Generate a token: `argocd account generate-token --account ccluster-cp`.
4. Store it: `kubectl -n <argocd-namespace> create secret generic ccluster-cp-argocd-token
   --from-literal=token=<token>`.
5. If argocd-server's Service is not named `argocd-server`, pass its name with
   `--argocd-server-service`.

The same steps recover a bundled installation whose token and initial admin Secret are
both gone; the hook Job then logs them instead of creating a token.

### Updating and removing an installation

After loading a rebuilt image with `make load-cp`, restart the deployment when the image tag
and Deployment spec are unchanged; `make deploy-cp` alone will not restart it:

```bash
kubectl -n ccluster-cp-system rollout restart deploy/ccluster-cp-controller-manager
```

`make undeploy-cp` removes the operator and bundled Argo CD, but keeps CRDs through
`helm.sh/resource-policy: keep`. Complete CR deletion/finalization while the controllers
are still running. `make uninstall` deletes CRDs and therefore every CR of those kinds.

To stop only the operator while keeping Argo CD running:

```bash
kubectl -n ccluster-cp-system scale deploy/ccluster-cp-controller-manager --replicas=0
```
