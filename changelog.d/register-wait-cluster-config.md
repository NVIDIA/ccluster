### Changed
- `ccluster register`, in Go and Python, waits until the control plane has reached the
  registered cluster, the CCLInfraPlane's `ClusterConfigApplied` condition being `True`, and
  prints the cluster's domain. If that takes over two minutes it fails with the condition's
  reason and message, such as `ClusterUnreachable` for a `--server` the control plane cannot
  reach, instead of reporting success. The quickstart's `register.py` and Go `register`
  wait for `ClusterConfigApplied` rather than `Available`.
