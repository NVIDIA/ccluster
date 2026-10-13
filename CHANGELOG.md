# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project aims to adhere to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.0.6] - 2026-10-12

### Added
- `ccluster-py` installs a `ccluster` command built on click. `ccluster init -cp CONTEXT`
  saves the control plane's kubeconfig context after checking it serves the ccluster API.
  `ccluster register NAME -n NS -ip CONTEXT [--server URL]` then creates a cluster-admin
  ServiceAccount token on the infra-plane cluster and registers it with that control plane as
  the CCLInfraPlane NAME.
  `ccluster unregister NAME -n NS` deletes the CCLInfraPlane and its credentials Secret,
  leaving the infra plane's ServiceAccount and token in place.
- `CCLComponentSet` status gains `componentSyncs`, which reports Argo CD's health and sync
  verdict for each released component and whether that verdict was reached on the spec the
  operator wrote.
- The first ccluster-ip-operator controller: it runs every `catalogs/cluster_base`
  component's `Verifier` against the `CCLClusterConfig` and reports the outcome as its
  `Verified` condition, naming each failing chart with the error its Verifier returned.
  Each `cluster_base` chart is registered in the operator's verifier registry
  (`internal/controller/ip`) with a check its chart package defines; every chart starts
  with one that always passes, and real checks follow. The ip ClusterRole is now generated from `internal/controller/ip`.
- `dynamo_deployment` gains `kvTransfer`, which selects the NIXL backend of the
  disaggregated pools, UCX or LIBFABRIC. LIBFABRIC requires `kvTransfer.efa`, which gives
  every worker one EFA device through DRA.
- `dynamo_deployment` and `inference_deployment` gain `volumeMounts`. Dynamo mounts the
  claims into the Frontend and every worker.

### Changed
- `inference_deployment` renders `hpa.cooldownPeriod: 0` as 1800, as the platform chart
  does.
- `inference_deployment` sidecars emit only inbound `downstream_rq` stats.
- The Applications the operator writes carry their Helm values as `source.helm.values`, a
  YAML document, instead of `source.helm.valuesObject`. Argo CD renders the two
  identically. Existing Applications are rewritten on the next reconcile; Argo CD compares
  the rewritten source, which renders the same manifests, so the rewrite alone starts no
  sync.

### Removed
- **Breaking:** `supplementalGroupIds` is removed from the `compute_deployment`,
  `inference_deployment` and `job_deployment` values. It added the host GIDs that AMD GPU
  device nodes need, and no supported accelerator uses it. `inference_deployment` now
  renders no pod `securityContext`. With the list empty it rendered
  `securityContext: null`, which server-side apply rejects, so Argo CD never created the
  Rollout.
- **Breaking:** `compute_deployment` drops `sshUsername`, `sshPassword` and
  `sshGithubUsername`, and the Secret that held the password. `sshPublicKey` is now
  required and is the only way to log in, as it already was for every compute deployment
  the platform creates.

### Fixed
- A CCLComponentSet no longer releases the next component on a `Healthy`/`Synced` verdict
  left over from the previous spec: it waits until Argo CD's `status.sync.comparedTo`
  names the spec the operator wrote and no operation, including a PostSync hook, is
  pending or running. A failed sync of that spec, including a failed hook, is reported as
  `WaveSyncFailed`.
- The `inference_deployment` KEDA query counts only the deployment's own pods. It summed
  the in-flight requests of every inference deployment in the namespace on the same port.
- `dynamo_deployment` and `inference_deployment` render an `hpa.averageValue` outside
  1-2^53 as 2^53 instead of passing it to KEDA.
- `job_deployment` no longer sets `ttlSecondsAfterFinished: 7200` on its Job. After the
  TTL controller deleted a finished Job, self-heal created it again and it ran again. The
  finished Job now stays until the CCLComponentSet is deleted; its pods stay unless pod
  garbage collection deletes them first.

## [0.0.5] - 2026-10-05

### Added
- `CCLClusterConfig`, a cluster-scoped singleton describing the infra-plane cluster the
  ip operator runs in (its domain). The ip chart ships this CRD; the cp chart does not.
- Release artifacts carry their license payload. The operator images ship `LICENSE`,
  `THIRD_PARTY_NOTICES.md` and the license and NOTICE files of every linked Go module
  under `/licenses`; the packaged Helm charts include `LICENSE` and
  `THIRD_PARTY_NOTICES.md`; the `ccluster-py` wheel and sdist include `LICENSE`.
  `THIRD_PARTY_NOTICES.md` now also lists the Go module licenses and the argo-cd chart
  the cp chart bundles.

### Removed
- **Breaking:** The `cluster_base/teleport_kube_agent` component, its Go package and its
  `ccluster.catalogs.cluster_base.teleport_kube_agent` Python model. It wrapped the
  AGPL-3.0 upstream chart, the catalog's only non-permissive dependency, and needed a
  caller-run Teleport cluster; deploy the upstream chart directly instead.
  `cluster_base.Components` no longer includes it.

## [0.0.4] - 2026-09-30

### Added
- `CCLClient.is_component_set_finished(*, name)` reports whether a set is applied and
  reported converged, with one read. It requires successful validation and Application
  writes, completion conditions for the current generation, and a ComponentSet that is
  not being deleted.
- `CCLClient.get_component_set(*, name)` and `CCLClient.get_infraplane(*, name)` read one
  object and return it as its model, raising `NotFoundError` if it does not exist.
- `component_set_current_conditions(component_set)` returns the conditions the operator
  computed from the object's current generation, keyed by type, so a caller can read the
  reason and message behind a rollout that has not finished.
- The operator and catalog `Chart.yaml` files carry `home`, `sources` and `maintainers`,
  and every `cluster_base` chart has a description. `THIRD_PARTY_NOTICES.md` records
  teleport-kube-agent as AGPL-3.0, fetched from upstream by Argo CD rather than
  redistributed.

### Changed
- **Breaking:** `CCLClient(namespace, api_client=None)` becomes
  `CCLClient(namespace, *, api_client)`. `api_client` is required and there is no
  in-cluster default, so the caller decides which connection its clients share. Clients
  built on the same `api_client` reuse its API discovery results, so a client per
  namespace costs nothing more.
- **Breaking:** Constructing a `CCLClient` reads and writes no object: it no longer
  creates its namespace. `CCLClient.ensure_namespace()` does, and is the only method that
  does, so a mistyped namespace fails against the API server rather than quietly gaining a
  working infra plane.
- **Breaking:** `CCLClient.create_infraplane` and `CCLClient.create_component_set` are
  renamed `apply_infraplane` and `apply_component_set`. Both server-side apply, so they
  create the object or update it in place; the old names said only the first.
- **Breaking:** `CCLClient` methods take the object's name by keyword only, for example
  `delete_component_set(name="local-base")`.

### Fixed
- Application and AppProject reconciliation no longer deletes the Argo CD status and any
  pending operation on the objects it manages: the managed spec is replaced with a patch
  instead of a whole-object update. Explicit null Helm values survive a spec rewrite.
- A CCLComponentSet waits for a referenced CCLInfraPlane whose status is still the
  previous generation's, rather than trusting an `Available` condition computed for an
  older spec. The status reports that separately from a plane that is not Available.
- A write to an Application or AppProject built on a stale read is retried in place
  instead of failing the reconcile. Neither object has a status subresource, so Argo CD's
  own status writes move the resource version the operator's patch carries, and a read
  from the informer cache can miss an object that already exists. Neither lost race
  surfaces as a transient `ApplicationFailed` condition on the CCLComponentSet any more.
- Pyright and Pylance no longer report `apiVersion` as a missing argument when a
  `CCLInfraPlane` or `CCLComponentSet` is constructed: `CustomResource.api_version` passes
  its default to `Field` by keyword, the only form type checkers read as a default.

## [0.0.3] - 2026-09-16

### Added
- `catalogs/deployments/`, `catalogs/resources/` and `catalogs/namespace_base/`: the
  tenant charts ported from the legacy Python catalog, one version of each and without
  the cserve variants. `inference_deployment` (from `inference_deployment_v3`),
  `compute_deployment`, `job_deployment` and `dynamo_deployment` are the workloads;
  `kai_gpu_queues` and `persistent_volume` are the GPU quota queues and tenant volumes
  they reference through `kaiQueue` and `volumeMounts`; `namespace_base` is the
  namespace baseline they assume. Each pairs a Helm chart with a Go package (`Values`,
  `Validate`, `New`) and generated Pydantic models under `ccluster.catalogs`. The charts
  take camelCase values, scope everything to the release namespace instead of a user id,
  and read plain values in place of the base64 fields the Argo CD `--set` path needed.
- `catalogs/components`: the registry of every chart in the catalog. Adding a chart is
  now a directory with a `component.go`, one line in `components.go` and
  `make python-generate`; a test fails when the list and the tree disagree.
- `CCLClient.create_component_set(name, components)` and
  `CCLClient.delete_component_set(name)` in `ccluster-py`. `create_component_set`
  server-side applies a `CCLComponentSet` in the client's namespace from a list of
  `ComponentSpec`s built with `ccluster.catalogs.component`, so calling it again updates
  the set in place; `delete_component_set` returns as soon as the delete is accepted and
  leaves the operator to uninstall the set's charts in reverse, one at a time.
  `python/examples/create_component_set.py` drives both from the command line.

### Changed
- `CCLClient.register_cluster` and `unregister_cluster` are renamed
  `create_infraplane` and `delete_infraplane`, after the `CCLInfraPlane` they create and
  remove and matching `create_component_set` and `delete_component_set`.
  `python/examples/register_cluster.py` is now `create_infraplane.py`.
- `ccluster-py` models a CRD's `spec` and `status` only: `CCLInfraPlaneSpec`,
  `CCLInfraPlaneStatus`, `CCLComponentSetSpec`, `CCLComponentSetStatus` and their nested
  types, in `ccluster.v1alpha1.<kind>`, generated straight from the CRD schema by
  `hack/pyschema` and datamodel-code-generator, constraints and docstrings included.
  `ccluster.v1alpha1.base` is a hand-written generic `CustomResource[Spec, Status]`
  template with a small `ObjectMeta`; `ccluster.v1alpha1` itself is generated from the
  CRD kinds with the `Kind` enum and one concrete `CustomResource` per kind
  (`CCLComponentSet`, `CCLInfraPlane`), which `CCLClient.create_infraplane` and
  `create_component_set` build and return. `hack/pyschema` now reflects chart `Values`
  only and shapes no Python; the chart packages are written by the generator with
  `CHART` appended, and the catalog and version directories are namespace packages.

## [0.0.2] - 2026-09-11

### Added
- `ccluster.client.CCLClient` in `ccluster-py`: `register_cluster(name, server=None,
  bearer_token=None, ca_cert=None)` creates an Argo CD-shaped credentials Secret and the
  `CCLInfraPlane` that points at it, and `unregister_cluster(name)` removes both. The
  client writes into one namespace, creating it if needed, and connects with the
  in-cluster configuration unless given an `ApiClient`. With no `server`, `bearer_token`
  or `ca_cert`, `register_cluster` registers the cluster the operator itself runs in,
  credential-free, using Argo CD's special-cased in-cluster server address.
  `python/examples/register_cluster.py` drives it from the command line. `ccluster-py`
  now depends on the `kubernetes` package.

### Changed
- Charts published from `main` are versioned `X.Y.Z-dev.<sha>`, where `X.Y.Z` is the
  `VERSION` file with the patch bumped (`0.0.2-dev.<sha>` after `v0.0.1`), instead of
  `0.0.0-g<sha>`. Dev builds now sort between the release they follow and the next one.
- `make build-installer-*` (and so `make build` / `make helm-generate`) no longer rewrites
  `config/{cp,ip}/kustomization.yaml`. The overlays commit the image name
  `ghcr.io/nvidia/ccluster-{cp,ip}-operator` and set no tag; the deployed image is
  `manager.image` in the chart's `values.yaml`, stamped at `make helm-package`, so the
  kustomize image was dead output that only dirtied a tracked file.
- The chart values toggles follow the plugin's new naming: `crd.enabled`,
  `metrics.enabled`, `certManager.enabled`, `prometheus.enabled`, `rbac.helpers.enabled`
  (was `rbacHelpers.enable`) and, for consistency, `argo-cd.enabled`. New keys with the
  plugin's defaults: `manager.enabled`, `manager.terminationGracePeriodSeconds`,
  `metrics.secure`, `rbac.namespaced`, `serviceAccount.*`, `networkPolicy.enabled`. The
  unused `manager.env` and `manager.envOverrides` keys are gone. The rendered manifests are
  otherwise unchanged, except that an empty `manager.image.tag` now falls back to
  `Chart.appVersion`.

### Fixed
- `manager.imagePullSecrets` in the `ccluster-cp` and `ccluster-ip` charts is now rendered
  into the manager Deployment's pod spec. It was declared in `values.yaml` but no template
  consumed it, so a chart installed from a cluster that needs credentials for the private
  `ghcr.io/nvidia` images went `ImagePullBackOff` with the value silently ignored. The
  charts are regenerated with kubebuilder v4.15.0, whose `helm/v2-alpha` plugin emits the
  block; the Makefile now pins and downloads that version instead of using whatever is on
  `PATH`.

## [0.0.1] - 2026-09-04

First release. Everything below is new; the entries that described intermediate states
of the unreleased tree (the `CCLDeployment` to `CCLComponentSet` rename, the move from
kustomize to Helm deploy targets, and fixes to code that never shipped) are folded into
the description of what ships.

### Added
- `CCLInfraPlane` (`ccluster.nvidia.com/v1alpha1`), which registers an infra-plane cluster
  with Argo CD. The control-plane operator creates one Argo CD `AppProject` per namespace,
  restricted to the catalog repository, and mirrors the Secret named by
  `spec.clusterCredentialsRef` into the Argo CD namespace as a project-scoped cluster
  Secret named `<namespace>-<name>`. The project is removed by the last CCLInfraPlane in
  the namespace.
- `CCLComponentSet` (`ccluster.nvidia.com/v1alpha1`), which rolls out an ordered bundle of
  catalog charts onto registered clusters. Each component becomes one Argo CD
  `Application` in the CCLComponentSet's own namespace under that namespace's project; a
  component's Application is not created until every component before it reports Healthy
  and Synced, and teardown runs in reverse, one at a time. Progress is reported on
  `status.conditions` as `Validated`, `ComponentsApplied` and `Progressing`. The
  controller starts and reports `ApplicationCRDMissing` when Argo CD is not installed,
  and picks up the Application watch on restart once it is.
- `catalogs/cluster_base`, a pure-Helm catalog of 18 platform components (Istio,
  cert-manager, SPIRE, KEDA, GPU Operator, kube-prometheus-stack, Longhorn, external-dns,
  Fluent Bit, Argo Rollouts and more), which is what `CCLComponentSet` component `chart`
  paths resolve against. The repository and revision are operator configuration
  (`--catalog-repo-url`, `--catalog-revision`), settable at deploy time with
  `make deploy-cp CATALOG_REPO_URL=... CATALOG_REVISION=...`.
- Go packages describing each `catalogs/cluster_base` chart's Helm values as typed structs,
  under `github.com/NVIDIA/ccluster/catalogs/...`. Each exports
  `New(namespace, values) (v1alpha1.ComponentEntry, error)`, building the API type
  directly; `catalogs/cluster_base.Components(infraPlane)` returns a sample set of all
  eighteen in rollout order to copy and edit. `teleport_kube_agent` validates its required
  values rather than installing an agent that cannot connect. These are a convenience for
  Go callers, not a validation layer: the CRD's `values` field remains untyped.
- `ccluster-py`, a generated Pydantic v2 rendering of the CRDs and of every catalog
  component's values, published to PyPI. Regenerated by `make python-generate`; CI fails
  on drift from the Go types.
- Helm charts `ccluster-cp` and `ccluster-ip` for the two operators, generated from the
  kustomize overlays by the kubebuilder `helm/v2-alpha` plugin and published to
  `oci://ghcr.io/nvidia/charts`. `make deploy-cp` / `deploy-ip` install them with
  `helm upgrade --install`; `make undeploy-*` uninstalls but keeps the CRDs
  (`helm.sh/resource-policy: keep`), so custom resources survive.
- Argo CD as a pinned dependency of the control-plane chart, installed alongside the
  operator by `make deploy-cp` and never by the operator itself. Skip it with
  `make deploy-cp ARGOCD=false`; bump it with `make helm-deps-update-cp`. The bundled
  application controller runs with `controller.diff.server.side` to match the
  `ServerSideApply` sync option on every generated Application, so charts with very large
  CRDs converge instead of reporting OutOfSync forever.
- `make helm-adopt-crds-cp`, which hands CRDs applied by `make install` over to the Helm
  release without deleting anything.
- Release pipeline. Every merge to `main` publishes both operator images (`:<sha>`) and
  both charts (`0.0.0-g<sha>`) to GHCR. A release is a reviewed PR titled
  `release: new ccluster version vX.Y.Z` that updates this file and the root `VERSION`
  file, followed by a GitHub Release on its merge commit, which retags the images as
  `X.Y.Z`, re-packages the charts, publishes `ccluster-py==X.Y.Z` to PyPI and attaches
  the assets. The workflow refuses a tag that disagrees with `VERSION`, and PRs carrying
  the `release` label run the full e2e tier in CI.
- Agent skills under `.agents/skills/` (`commit-pr`, `release`, `pr-review`) that write
  down the commit, release and review procedures for any coding agent; `.claude/skills`
  symlinks to the same directory.
- Two-tier end-to-end suite. `make test-e2e` deploys with `ARGOCD=false` into a
  disposable kind cluster and covers RBAC, watch wiring and the status paths reachable
  without Argo CD; `make test-e2e-full` upgrades the same release to real Argo CD and
  proves ordered release, reverse teardown, `selfHeal` drift correction, a values change
  rolling forward, and the AppProject as a tenancy boundary.
- `DESIGN.md`, describing the control-plane design and the alternatives rejected along
  the way, notably Argo CD `ApplicationSet` generators and the `RollingSync` strategy.
- Legacy `catalog` package: Helm charts paired with typed Pydantic value schemas for the
  cluster base components and the workload charts `inference_deployment_v3`,
  `cserve_deployment_v3`, `compute_deployment`, `job_deployment` and `namespace_base`.
  Being retired in favour of `catalogs/` and `ccluster-py`.
- Community-health files (Code of Conduct, Contributing, Security, Support, Maintainers).

<!--
Template for future releases:

## [X.Y.Z] - YYYY-MM-DD
### Added
### Changed
### Deprecated
### Removed
### Fixed
### Security
-->

[Unreleased]: https://github.com/NVIDIA/ccluster/compare/v0.0.6...HEAD
[0.0.6]: https://github.com/NVIDIA/ccluster/compare/v0.0.5...v0.0.6
[0.0.5]: https://github.com/NVIDIA/ccluster/compare/v0.0.4...v0.0.5
[0.0.4]: https://github.com/NVIDIA/ccluster/compare/v0.0.3...v0.0.4
[0.0.3]: https://github.com/NVIDIA/ccluster/compare/v0.0.2...v0.0.3
[0.0.2]: https://github.com/NVIDIA/ccluster/compare/v0.0.1...v0.0.2
[0.0.1]: https://github.com/NVIDIA/ccluster/releases/tag/v0.0.1
