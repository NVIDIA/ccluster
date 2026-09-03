# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project aims to adhere to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
- Release pipeline. Every commit on `main` publishes both operator images to GHCR tagged
  with its commit hash; pushing a `vX.Y.Z` tag retags those images as `X.Y.Z`, pushes both
  charts to `oci://ghcr.io/nvidia/charts`, publishes the `ccluster` Python package to PyPI
  and creates a GitHub Release. The tag is the only version: nothing in the tree is bumped.
- Initial public release of the `catalog` package: Helm charts paired with typed
  Pydantic value schemas for cluster base components and user workloads.
- `cluster_base` platform stack (Istio, cert-manager, SPIRE, KEDA, GPU Operator,
  kube-prometheus-stack, Longhorn, external-dns, Fluent Bit, Argo Rollouts, and more).
- Workload charts: `inference_deployment_v3`, `cserve_deployment_v3`,
  `compute_deployment`, `job_deployment`, `namespace_base`.
- Community-health files (Code of Conduct, Contributing, Security, Support, Maintainers).
- `CCLInfraPlane` (`ccluster.nvidia.com/v1alpha1`), which registers an infra-plane cluster
  with Argo CD by mirroring the Secret named by `spec.clusterCredentialsRef` into the Argo CD
  namespace as a cluster Secret, under the resource's own name.
- `CCLComponentSet` (`ccluster.nvidia.com/v1alpha1`), which rolls out an ordered bundle of
  catalog charts onto registered clusters. Each component becomes one Argo CD `Application`;
  a component's Application is not created until every component before it reports Healthy
  and Synced, and teardown runs in reverse, one at a time. Progress is reported on
  `status.conditions` as `Validated`, `ComponentsApplied` and `Progressing`.
- `catalogs/cluster_base`, a pure-Helm catalog of 18 platform components, which is what
  `CCLComponentSet` component `chart` paths resolve against. The repository and revision are
  operator configuration (`--catalog-repo-url`, `--catalog-revision`), settable at deploy
  time with `make deploy-cp CATALOG_REPO_URL=... CATALOG_REVISION=...`.
- Go packages describing each `catalogs/cluster_base` chart's Helm values as typed structs,
  under `github.com/NVIDIA/ccluster/catalogs/...`. Each exports
  `New(namespace, values) (v1alpha1.ComponentEntry, error)`, building the API type directly.
  A component knows only its chart directory: the name, namespace and rollout position of
  each component are the caller's. `catalogs/cluster_base.Components(infraPlane)` returns a
  sample set of all eighteen in rollout order — reproducing the sync waves of the legacy
  Python catalog — to copy and edit. `teleport_kube_agent` is the one chart with values it
  cannot be installed without — they ship as placeholders — and its `New` reports them
  rather than returning an entry that installs an agent which cannot connect.

  These are a convenience for callers building a `CCLComponentSet` in Go, **not** a validation
  layer: the CRD's `values` field remains untyped, and a hand-written `CCLComponentSet` is still
  forwarded to Helm verbatim.
- `DESIGN.md`, describing the control-plane design and the alternatives rejected along the
  way — notably Argo CD `ApplicationSet` generators and the `RollingSync` sync strategy.
- `controller.diff.server.side` enabled on the bundled Argo CD application controller, to
  match the `ServerSideApply` sync option every generated Application carries. Without both,
  charts containing very large CRDs report OutOfSync forever and stall a rollout.
- Helm charts for both operators under `charts/cp/chart` and `charts/ip/chart`, generated
  from the kustomize overlays by the kubebuilder `helm/v2-alpha` plugin. Regenerate with
  `make helm-generate`; CI fails if the committed charts drift from `config/`.
- Argo CD as a pinned dependency of the control-plane chart, installed alongside
  `ccluster-cp-operator` by `make deploy-cp`. Argo CD is deliberately *not* installed by the
  operator at runtime. Skip it with `make deploy-cp ARGOCD=false`; bump it with
  `make helm-deps-update-cp`.
- `make helm-adopt-crds-cp`, a one-time migration for clusters whose CRDs were applied by
  `make install` or the old kustomize-based `make deploy-cp`. Helm refuses to adopt
  resources it does not own; this labels and annotates them into the release without
  deleting anything.

### Changed
- **Breaking:** the `CCLDeployment` kind is renamed `CCLComponentSet` (plural `cclcomponentsets`),
  since it is an ordered set of catalog components rather than a workload, and "Deployment"
  collided with the core Kubernetes kind. The back-reference annotations and label on the
  generated Applications move from `ccluster.nvidia.com/deployment-{name,namespace}` to
  `ccluster.nvidia.com/componentset-{name,namespace}`. There is no conversion or alias:
  existing `CCLDeployment` objects must be recreated as `CCLComponentSet`, and Applications
  stamped with the old annotations are no longer recognised as owned.
- `ComponentSpec`'s `chart`, `namespace` and `values` fields are grouped into a
  `ComponentEntry` struct, embedded inline. **No wire-format change:** the embed is anonymous,
  so all three remain component-level fields and the generated CRD schema is byte-identical.
- **Breaking:** `make deploy-cp` / `deploy-ip` / `undeploy-cp` / `undeploy-ip` now use
  `helm upgrade --install` / `helm uninstall` instead of `kustomize build | kubectl apply`.
  They require `helm` on `PATH`. Resource names and namespaces are unchanged.
- `make build-installer-cp` / `build-installer-ip` are no longer a deploy path — the
  `dist/install-{cp,ip}.yaml` they produce are the input the charts are generated from.
- Documentation now describes the operators first: `README.md` leads with the control-plane
  API, and `AGENTS.md` covers the controllers, their RBAC, and the real test coverage.
- `make undeploy-cp` no longer deletes the CRDs, and therefore no longer deletes every
  custom resource in the cluster: the chart annotates them `helm.sh/resource-policy: keep`.
  It does now remove Argo CD, which is part of the same release.
- The control-plane chart's Argo CD dependency is now verified by rendering it with
  `helm template` in CI (`.github/workflows/test-chart.yml`) rather than by installing it
  into a kind cluster. The argoproj CRDs and `controller.diff.server.side` are asserted on
  every pull request instead of in a local-only target.
- `test/e2e` now splits into a quick tier and a full tier via Ginkgo `Label()` +
  `-ginkgo.label-filter`. `make test-e2e` (unchanged as the CI default) deploys with
  `ARGOCD=false` and stays fast — `ApplicationCRDMissing` is reachable again, and the
  `argoproj.io/applications` RBAC rule is covered by rendering just the Application CRD out of
  the vendored dependency chart, no real Argo CD required. `make test-e2e-full` (or
  `E2E_FULL=true`) deploys real Argo CD instead and asserts a real catalog component reaches
  `Healthy`/`Synced`, a two-component deployment releases in order and tears down in reverse,
  and a live resource drifted out from under Argo CD gets reverted by `selfHeal` — all under
  genuine reconciliation. This replaces `make test-e2e-argocd`, which never actually checked
  that a rollout worked — only that the argoproj CRDs existed — and which no CI workflow ran
  anyway. The full tier is opt-in only, same as `test-e2e-argocd` was, but now actually proves
  the things that target's name implied.

### Fixed
- The CCLComponentSet controller no longer refuses to start when Argo CD is absent. Its watch
  on `Application` is registered only if the CRD is served, because controller-runtime
  blocks a controller until every watch source syncs and a source for an unserved kind never
  does. Previously `make deploy-cp ARGOCD=false` — or simply starting the operator before
  Argo CD — left the controller reconciling nothing while logging "if kind is a CRD, it
  should be installed before calling Start" every ten seconds, which also made the
  `ApplicationCRDMissing` status unreachable. Install Argo CD after the operator and the
  operator must be restarted to pick up the watch — the quick e2e tier now exercises exactly
  this sequence end to end (see above).
- `releaseCount` no longer returns 1 for a CCLComponentSet with no components, which made the
  caller slice an empty component list and panic. The `MinItems=1` CRD marker still rejects
  such objects; the controller no longer depends on it for memory safety.

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
