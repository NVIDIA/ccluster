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
- Helm charts for both operators under `charts/cp/chart` and `charts/ip/chart`, generated
  from the kustomize overlays by the kubebuilder `helm/v2-alpha` plugin. Regenerate with
  `make helm-generate`; CI fails if the committed charts drift from `config/`.
- Argo CD as a pinned dependency of the control-plane chart, installed alongside
  `ccluster-cp-operator` by `make deploy-cp`. Argo CD is deliberately *not* installed by the
  operator at runtime. Skip it with `make deploy-cp ARGOCD=false`; bump it with
  `make helm-deps-update-cp`.
- `make test-e2e-argocd`, which installs the control-plane chart with Argo CD enabled into a
  kind cluster and verifies the argoproj CRDs appear.
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
- `make undeploy-cp` no longer deletes the CRDs, and therefore no longer deletes every
  custom resource in the cluster: the chart annotates them `helm.sh/resource-policy: keep`.
  It does now remove Argo CD, which is part of the same release.

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
