### Fixed
- `cluster_base/gpu_operator` now creates the ResourceQuota `gpu-operator-critical-pods` in
  its namespace, covering the `system-node-critical` and `system-cluster-critical` priority
  classes. On GKE, which admits pods of those classes outside `kube-system` only in a
  namespace with such a quota, gpu-operator and node-feature-discovery pods were rejected and
  the component never became Healthy. The new `quota.criticalPodLimit` value sets the quota's
  pod limit, 2048 by default.
