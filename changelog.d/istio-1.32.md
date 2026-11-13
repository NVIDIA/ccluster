### Added
- `catalogs/cluster_base/istio` takes `istio-cni.tolerations` (`IstioCNI.Tolerations`), which
  replaces the istio-cni-node DaemonSet's tolerations; an empty list removes them all. Unset,
  the DaemonSet keeps the upstream wildcard tolerations.

### Changed
- **Breaking:** `catalogs/cluster_base/istio` upgrades Istio from 1.24.6 to 1.32.0, which
  supports Kubernetes 1.33 to 1.37. The chart pulls its subcharts from
  `https://blob.istio.io/istio-release/charts`, because Istio stopped publishing to
  `istio-release.storage.googleapis.com` at 1.31. Sidecars are now injected as native
  sidecars, the Istio default since 1.27, so `istio-proxy` runs as an init container. Istio
  does not support an in-place upgrade across this many minor versions.

### Removed
- **Breaking:** `catalogs/cluster_base/istio` drops `ciliumIstioPatch`. Enabled, it rendered
  a second, partial `istio-cni-node` DaemonSet in `kube-system`, which the API server rejects,
  so the Application could not sync and the real DaemonSet in the component's namespace kept
  its wildcard tolerations. On Cilium, set `istio-cni.tolerations` without the wildcards, so
  that istio-cni-node waits for cilium-agent.
