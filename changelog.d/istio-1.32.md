### Changed
- **Breaking:** `catalogs/cluster_base/istio` upgrades Istio from 1.24.6 to 1.32.0, which
  supports Kubernetes 1.33 to 1.37. The chart pulls its subcharts from
  `https://blob.istio.io/istio-release/charts`, because Istio stopped publishing to
  `istio-release.storage.googleapis.com` at 1.31. Sidecars are now injected as native
  sidecars, the Istio default since 1.27, so `istio-proxy` runs as an init container. Istio
  does not support an in-place upgrade across this many minor versions.
