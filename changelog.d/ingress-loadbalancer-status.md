### Added
- The ip operator reports the cluster's LoadBalancers in a new
  `CCLClusterConfig.status.loadBalancers`: every Service of type `LoadBalancer` labelled
  `ccluster.nvidia.com/loadbalancer: <name>`, listed by name with its `ip` and `hostname`.
  It watches those Services, so a newly assigned address shows at once. Two Services with
  the same name are left out and reported as `Verified=False/LoadBalancerConflict`. The cp
  operator copies the list into `CCLInfraPlane.status.clusterConfig`, and both kinds show
  the ingress IP in an `Ingress` column. The ip operator's ClusterRole gains read access to
  Services.

### Changed
- `catalogs/cluster_base/ingress_nginx` now enables the controller Service as a
  `LoadBalancer` labelled `ccluster.nvidia.com/loadbalancer: ingress`, so an infra plane
  gets a cloud load balancer on its next sync. The typed `controller.service` value is
  removed: the Service is fixed in the chart's values.
