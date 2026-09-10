# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project aims to adhere to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
- `ccluster.client.CCLClient` in `ccluster-py`: `register_cluster(name, server,
  bearer_token, ca_cert)` creates an Argo CD-shaped credentials Secret and the
  `CCLInfraPlane` that points at it, and `unregister_cluster(name)` removes both. The
  client writes into one namespace and defaults to the in-cluster configuration and the
  pod's own namespace. `ccluster-py` now depends on the `kubernetes` package.

### Changed
- Charts published from `main` are versioned `X.Y.Z-dev.<sha>`, where `X.Y.Z` is the
  `VERSION` file with the patch bumped (`0.0.2-dev.<sha>` after `v0.0.1`), instead of
  `0.0.0-g<sha>`. Dev builds now sort between the release they follow and the next one.
- `make build-installer-*` (and so `make build` / `make helm-generate`) no longer rewrites
  `config/{cp,ip}/kustomization.yaml`. The overlays commit the image name
  `ghcr.io/nvidia/ccluster-{cp,ip}-operator` and set no tag; the deployed image is
  `manager.image` in the chart's `values.yaml`, stamped at `make helm-package`, so the
  kustomize image was dead output that only dirtied a tracked file.

### Fixed
- `manager.imagePullSecrets` in the `ccluster-cp` and `ccluster-ip` charts is now rendered
  into the manager Deployment's pod spec. It was declared in `values.yaml` but no template
  consumed it, so a chart installed from a cluster that needs credentials for the private
  `ghcr.io/nvidia` images went `ImagePullBackOff` with the value silently ignored. The
  charts are regenerated with kubebuilder v4.15.0, whose `helm/v2-alpha` plugin emits the
  block; the Makefile now pins and downloads that version instead of using whatever is on
  `PATH`.

### Changed
- The chart values toggles follow the plugin's new naming: `crd.enabled`,
  `metrics.enabled`, `certManager.enabled`, `prometheus.enabled`, `rbac.helpers.enabled`
  (was `rbacHelpers.enable`) and, for consistency, `argo-cd.enabled`. New keys with the
  plugin's defaults: `manager.enabled`, `manager.terminationGracePeriodSeconds`,
  `metrics.secure`, `rbac.namespaced`, `serviceAccount.*`, `networkPolicy.enabled`. The
  unused `manager.env` and `manager.envOverrides` keys are gone. The rendered manifests are
  otherwise unchanged, except that an empty `manager.image.tag` now falls back to
  `Chart.appVersion`.

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

[Unreleased]: https://github.com/NVIDIA/ccluster/compare/v0.0.1...HEAD
[0.0.1]: https://github.com/NVIDIA/ccluster/releases/tag/v0.0.1
