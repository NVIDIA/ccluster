# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project aims to adhere to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
- `CCLClient.is_component_set_finished(name=...)` reports whether a set is applied and
  reported converged, with one read. It requires successful validation and Application
  writes, completion conditions for the current generation, and a ComponentSet that is
  not being deleted.
- `CCLClient.get_component_set(name=...)` and `CCLClient.get_infraplane(name=...)` read
  one object and return it as its model, raising `NotFoundError` if it does not exist.
- `component_set_current_conditions(component_set)` returns the conditions the operator
  computed from the object's current generation, keyed by type, so a caller can read the
  reason and message behind a rollout that has not finished.
- A full-tier e2e spec covers the field ownership contract with Argo CD: Argo CD must not
  write into the spec the operator owns, and the deployment history in its Application
  status must survive a spec rewrite rather than restart.

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
- **Breaking:** Every `CCLClient` method takes the object's name by keyword, for example
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

[Unreleased]: https://github.com/NVIDIA/ccluster/compare/v0.0.3...HEAD
[0.0.3]: https://github.com/NVIDIA/ccluster/compare/v0.0.2...v0.0.3
[0.0.2]: https://github.com/NVIDIA/ccluster/compare/v0.0.1...v0.0.2
[0.0.1]: https://github.com/NVIDIA/ccluster/releases/tag/v0.0.1
