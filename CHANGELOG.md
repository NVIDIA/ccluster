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
