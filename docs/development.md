# Development mechanics

Start with the [README local loop](../README.md#local-development) and
[agent guide](../AGENTS.md). Read [DESIGN.md](../DESIGN.md) before changing controller
behaviour. Test selection is in [Testing](testing.md); image and CI contracts are in
[Build and release](build-and-release.md).

## Adding a controller or API

The two-binary layout requires manual wiring. There is no `cmd/main.go`,
`config/default/`, `config/manager/` or top-level `config/rbac/`.

1. Scaffold with kubebuilder, then move the reconciler into `internal/controller/cp/`
   or `internal/controller/ip/` and fix its package name.
2. Add its `SetupWithManager` call to `setupControllers` in the corresponding
   `cmd/ccluster-*-operator/main.go`. Preserve the cp binary's kubebuilder scaffold markers.
3. Move any scaffolded `config/samples/` CR into `examples/<scenario>/` and remove the
   resurrected samples directory. Examples are not kustomize inputs.
4. Put RBAC markers in the controller's own operator package and regenerate:

```bash
make manifests generate
make helm-generate
make python-generate
```

Godoc on API types becomes CRD descriptions and Python schema documentation, so changes to
comments require generation too. In a host-manager loop, separately update the target
cluster's CRDs with `make install`; Helm installations receive them through chart deployment.
An old CRD schema can prune newly added fields on apply.

Keep leader-election IDs `cp.ccluster.nvidia.com` and `ip.ccluster.nvidia.com` distinct.

## Per-operator RBAC

`make manifests` generates CRDs over `./api/...` and cp RBAC over
`./internal/controller/cp/...`, into `config/crd/bases/` and `config/cp/rbac/role.yaml`.
Markers only grant rights to the operator whose package contains them.

The ip RBAC generation pass is disabled: controller-gen produces no file for a package
without markers. `config/ip/rbac/role.yaml` therefore contains hand-written `rules: []`.
When adding the first ip controller, remove that placeholder and enable the ip pass in
`manifests`. The overlays prefix the shared `manager-role` name for each operator.

## Helm generation

`config/base/` and `config/{cp,ip}/` are the source for operator deployments and RBAC.
`make helm-generate` builds `dist/install-{cp,ip}.yaml` and runs the pinned kubebuilder
Helm plugin to generate `charts/{cp,ip}/chart/`. The install YAML is generator input;
deploy through Helm.

- `Chart.yaml` and `values.yaml` are preserved. They hold dependencies and the pinned
  `fullnameOverride` values `ccluster-cp` / `ccluster-ip`, which tests and recipes use.
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
- Register the component in `catalogs/components/components.go`, the single list of
  every chart; `hack/pyschema` and the tests read it, and `TestAllCoversTheTree` fails if
  the list and the `catalogs/` tree disagree. The chart's path under `catalogs/` becomes
  its path under `ccluster.catalogs` in Python automatically, whether it sits in a
  catalog subdirectory or directly under `catalogs/`. A `cluster_base` component also goes into
  `catalogs/cluster_base/cluster_base.go`, which supplies a sample order and deployment
  choices, not a universally deployable default; teleport values need real configuration.
- Include a hand-written `.helmignore` excluding `*.go`, so source does not enter chart
  archives. This is separate from the generated operator-chart ignore files.
- Run `make python-generate` after changing values types or their doc comments.

## Python generation

`make python-generate` runs `hack/pyschema` to extract CRD JSON Schema and reflect catalog
`Values` with `invopop/jsonschema`, including Go comments. It then runs
`datamodel-code-generator` to emit Pydantic v2 modules.

Generated directories, deleted and rebuilt on each run:

- `python/src/ccluster/v1alpha1/`
- `python/src/ccluster/catalogs/cluster_base/`
- `python/src/ccluster/catalogs/deployments/`
- `python/src/ccluster/catalogs/resources/`
- `python/src/ccluster/catalogs/namespace_base/`

Hand-written files are `python/src/ccluster/__init__.py`,
`python/src/ccluster/catalogs/__init__.py`, `python/pyproject.toml`, `python/README.md`
and `python/tests/`. Generator changes belong in `hack/pyschema/` or the generation recipe,
not in emitted modules.

The export adds API version/kind defaults, a small typed `ObjectMeta`, and dictionary
values. Optional fields default to `None`; consumers use `exclude_none=True` to retain
explicit `False` while omitting unset values. See [Python usage](../python/README.md).
CI regenerates and checks for drift.

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
Tests use `KIND_CLUSTER_E2E=ccluster-test-e2e` and delete it on completion. Never point e2e
at a development or real cluster. `make clean` deletes both development clusters;
`make clean-all` also deletes `bin/` and `dist/`, requiring tools to be downloaded again.

### CRD ownership and Argo CD

`make deploy-cp` installs CRDs through Helm. Do not precede it with `make install`:
Helm refuses existing CRDs without its ownership labels and annotations. To adopt them
without deleting CRs, run `make helm-adopt-crds-cp`, then deploy. `make install` is for
the host-manager loop with `make run-cp`, where no chart is installed.

`make deploy-cp ARGOCD=false` skips the dependency when Argo CD already exists or is
intentionally absent. The operator must tolerate the Application CRD being absent at
startup. Argo CD clones `CATALOG_REPO_URL` itself; a private catalog requires credentials,
provided through `GITHUB_TOKEN` to `make deploy-cp`. Missing credentials surface as
Application comparison errors. Use `CATALOG_REVISION` to select the catalog commit or tag.

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
