# Third-Party Notices

CCluster packages and depends on third-party open-source software. This file
lists the third-party components redistributed in ccluster's release artifacts (the
operator images and the operator Helm charts) and those referenced as Helm chart
dependencies by the charts under `catalogs/`, together with their upstream source and
license. This list is provided for attribution and convenience; it is **not** a
substitute for the license text shipped with each upstream project.

> **Maintainers:** verify each entry's license against the exact pinned version
> before a release. Chart versions are pinned in `charts/cp/chart/Chart.yaml` and the
> corresponding `catalogs/**/Chart.yaml` files.

## Images

The `ccluster-cp-operator`, `ccluster-ip-operator` and `ccluster-compute-ssh` images
each contain one statically linked Go binary. It includes the Go standard library
(BSD-3-Clause) and Go modules licensed under Apache-2.0, BSD-2-Clause, BSD-3-Clause, MIT
and ISC. The image ships the full license
and NOTICE file of each of them under `/licenses/go`, collected from the exact module
versions at build time, together with this file and the project `LICENSE` under
`/licenses`. The Go module versions are pinned in [`go.mod`](go.mod) and
[`go.sum`](go.sum).

## Command line

Each `ccluster_<version>_<os>_<arch>.tar.gz` attached to a release holds the statically
linked `ccluster` binary, built from the same Go module and covered by the same licenses
as the images. The archive ships this file and the project `LICENSE` at its root and the
license and NOTICE file of each linked Go module and the Go standard library under
`licenses/go`.

## Operator Helm charts

The packaged `ccluster-cp` chart bundles the following charts. The `ccluster-ip` chart
bundles none.

| Component | Version | Upstream Helm repository | License |
| --- | --- | --- | --- |
| argo-cd | 10.4.0 | https://argoproj.github.io/argo-helm | Apache-2.0 |
| redis-ha (an argo-cd dependency, disabled by default) | 4.38.0 | https://dandydeveloper.github.io/charts/ | Apache-2.0 |
| external-dns (disabled by default) | 1.19.0 | https://kubernetes-sigs.github.io/external-dns/ | Apache-2.0 |

## Catalog Helm chart dependencies

| Component | Version | Upstream Helm repository | License |
| --- | --- | --- | --- |
| argo-rollouts | 2.37.7 | https://argoproj.github.io/argo-helm | Apache-2.0 |
| istio (base) | 1.32.0 | https://blob.istio.io/istio-release/charts | Apache-2.0 |
| istio (istiod) | 1.32.0 | https://blob.istio.io/istio-release/charts | Apache-2.0 |
| istio (cni) | 1.32.0 | https://blob.istio.io/istio-release/charts | Apache-2.0 |
| istio (gateway) | 1.32.0 | https://blob.istio.io/istio-release/charts | Apache-2.0 |
| cert-manager | v1.14.5 | https://charts.jetstack.io | Apache-2.0 |
| fluent-bit | 0.48.5 | https://fluent.github.io/helm-charts | Apache-2.0 |
| gpu-operator | v25.3.4 | https://helm.ngc.nvidia.com/nvidia | Apache-2.0 |
| ingress-nginx | 4.11.5 | https://kubernetes.github.io/ingress-nginx | Apache-2.0 |
| kai-scheduler | v0.15.3 | oci://ghcr.io/kai-scheduler/kai-scheduler | Apache-2.0 |
| keda | 2.19.0 | https://kedacore.github.io/charts | Apache-2.0 |
| kube-prometheus-stack | 73.1.0 | https://prometheus-community.github.io/helm-charts | Apache-2.0 |
| longhorn | 1.7.2 | https://charts.longhorn.io | Apache-2.0 |
| metrics-server | 3.12.2 | https://kubernetes-sigs.github.io/metrics-server/ | Apache-2.0 |
| network-operator | v24.4.0 | https://helm.ngc.nvidia.com/nvidia | Apache-2.0 |
| prometheus-adapter | 4.14.1 | https://prometheus-community.github.io/helm-charts | Apache-2.0 |

## Notes

- Catalog chart dependencies above are fetched from their upstream repositories at
  build/sync time; this repository pins their versions but does not vendor their
  source. The images those charts deploy carry their own licenses.
- Runtime dependencies of the `ccluster-py` package are declared in
  [`python/pyproject.toml`](python/pyproject.toml).
- If you add, remove, or bump a chart dependency, update this file in the same
  change.
