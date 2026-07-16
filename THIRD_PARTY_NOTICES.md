# Third-Party Notices

CCluster packages and depends on third-party open-source software. This file
lists the third-party components redistributed or referenced as Helm chart
dependencies by the `catalog` package, together with their upstream source and
license. This list is provided for attribution and convenience; it is **not** a
substitute for the license text shipped with each upstream project.

> **Maintainers:** verify each entry's license against the exact pinned version
> before a release. Chart versions are pinned in the corresponding
> `catalog/**/Chart.yaml` files. Entries flagged **VERIFY** below require
> particular attention.

## Helm chart dependencies

| Component | Version | Upstream Helm repository | License |
| --- | --- | --- | --- |
| argo-rollouts | 2.37.7 | https://argoproj.github.io/argo-helm | Apache-2.0 |
| istio (base) | 1.24.6 | https://istio-release.storage.googleapis.com/charts | Apache-2.0 |
| istio (istiod) | 1.24.6 | https://istio-release.storage.googleapis.com/charts | Apache-2.0 |
| istio (cni) | 1.24.6 | https://istio-release.storage.googleapis.com/charts | Apache-2.0 |
| istio (gateway) | 1.24.6 | https://istio-release.storage.googleapis.com/charts | Apache-2.0 |
| cert-manager | v1.14.5 | https://charts.jetstack.io | Apache-2.0 |
| external-dns | 1.19.0 | https://kubernetes-sigs.github.io/external-dns/ | Apache-2.0 |
| fluent-bit | 0.48.5 | https://fluent.github.io/helm-charts | Apache-2.0 |
| gpu-operator | v25.3.4 | https://helm.ngc.nvidia.com/nvidia | Apache-2.0 |
| ingress-nginx | 4.11.5 | https://kubernetes.github.io/ingress-nginx | Apache-2.0 |
| keda | 2.19.0 | https://kedacore.github.io/charts | Apache-2.0 |
| kube-prometheus-stack | 73.1.0 | https://prometheus-community.github.io/helm-charts | Apache-2.0 |
| longhorn | 1.7.2 | https://charts.longhorn.io | Apache-2.0 |
| metrics-server | 3.12.2 | https://kubernetes-sigs.github.io/metrics-server/ | Apache-2.0 |
| network-operator | v24.4.0 | https://helm.ngc.nvidia.com/nvidia | Apache-2.0 |
| opentelemetry-collector | 0.130.2 | https://open-telemetry.github.io/opentelemetry-helm-charts | Apache-2.0 |
| prometheus-adapter | 4.14.1 | https://prometheus-community.github.io/helm-charts | Apache-2.0 |
| spire | 0.20.0 | https://spiffe.github.io/helm-charts-hardened/ | Apache-2.0 |
| spire-crds | 0.4.0 | https://spiffe.github.io/helm-charts-hardened/ | Apache-2.0 |
| teleport-kube-agent | 18.2.5 | https://charts.releases.teleport.dev | **VERIFY** — Teleport Community Edition is licensed under AGPL-3.0 (recent versions); confirm the license and any distribution obligations for the pinned version before redistributing. |

## Notes

- Chart *dependencies* above are fetched from their upstream repositories at
  build/sync time; this repository pins their versions but does not vendor their
  source. The images those charts deploy carry their own licenses.
- Runtime Python dependencies (`pydantic`, `pyyaml`) are declared in
  [`catalog/pyproject.toml`](catalog/pyproject.toml) and are both permissively
  licensed (MIT).
- If you add, remove, or bump a chart dependency, update this file in the same
  change.
