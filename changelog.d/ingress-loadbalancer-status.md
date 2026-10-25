### Added
- The ip operator reports the cluster's ingress LoadBalancer in a new `LoadBalancerReady`
  condition on `CCLClusterConfig`: `Ready` with the Service and its addresses in the
  message, or `Pending`, `NotDeployed` or `Ambiguous`. It finds the Service by the
  `ccluster.nvidia.com/track-ingress=true` label and watches it, so a newly assigned
  address shows at once. The cp operator copies the condition into
  `CCLInfraPlane.status.clusterConfig`, and both kinds show it in a `LoadBalancer` column.
  The ip operator's ClusterRole gains read access to Services.

### Changed
- `catalogs/cluster_base/ingress_nginx` now enables the controller Service as a
  `LoadBalancer` labelled `ccluster.nvidia.com/track-ingress: "true"`, so an infra plane
  gets a cloud load balancer on its next sync. The typed `controller.service` value is
  removed: the Service is fixed in the chart's values.
