# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project aims to adhere to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
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
- `CCLDeployment` (`ccluster.nvidia.com/v1alpha1`), which rolls out an ordered bundle of
  catalog charts onto registered clusters. Each component becomes one Argo CD `Application`;
  a component's Application is not created until every component before it reports Healthy
  and Synced, and teardown runs in reverse, one at a time. Progress is reported on
  `status.conditions` as `Validated`, `ComponentsApplied` and `Progressing`.
- `catalogs/cluster_base`, a pure-Helm catalog of 18 platform components, which is what
  `CCLDeployment` component `chart` paths resolve against. The repository and revision are
  operator configuration (`--catalog-repo-url`, `--catalog-revision`), settable at deploy
  time with `make deploy-cp CATALOG_REPO_URL=... CATALOG_REVISION=...`.
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
- `make test-e2e` now installs real Argo CD instead of `ARGOCD=false`, and asserts a real
  catalog component reaches `Healthy`/`Synced` under Argo CD's own reconciliation. This
  replaces `make test-e2e-argocd`, which never actually checked that a rollout worked — only
  that the argoproj CRDs existed — and which no CI workflow ran anyway. The tradeoff:
  `ApplicationCRDMissing`, the status reported when Argo CD genuinely isn't installed, is no
  longer reachable in `test-e2e`; it is asserted at the unit tier instead
  (`ccldeployment_builder_test.go`).

### Fixed
- The CCLDeployment controller no longer refuses to start when Argo CD is absent. Its watch
  on `Application` is registered only if the CRD is served, because controller-runtime
  blocks a controller until every watch source syncs and a source for an unserved kind never
  does. Previously `make deploy-cp ARGOCD=false` — or simply starting the operator before
  Argo CD — left the controller reconciling nothing while logging "if kind is a CRD, it
  should be installed before calling Start" every ten seconds, which also made the
  `ApplicationCRDMissing` status unreachable. Install Argo CD after the operator and the
  operator must be restarted to pick up the watch.
- `releaseCount` no longer returns 1 for a CCLDeployment with no components, which made the
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
