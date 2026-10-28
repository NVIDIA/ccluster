### Added
- The ip operator reports the cluster's LoadBalancers in a new
  `CCLClusterConfig.status.loadBalancers`: every Service of type `LoadBalancer` labelled
  `ccluster.nvidia.com/loadbalancer: <name>`, listed by name with its `ip` and `hostname`.
  It watches those Services, so a newly assigned address shows at once. Two Services with
  the same name are left out and reported as `Verified=False/LoadBalancerConflict`. The cp
  operator copies the list into `CCLInfraPlane.status.clusterConfig`. The label's value
  is the chart's choice; neither operator gives any value a meaning. The ip operator's
  ClusterRole gains read access to Services.

### Changed
- `catalogs/cluster_base/ingress_nginx` now enables the controller Service as a
  `LoadBalancer` labelled `ccluster.nvidia.com/loadbalancer: ingress`, so an infra plane
  gets a cloud load balancer on its next sync.
- **Breaking:** the typed `ingress_nginx` values drop `controller.service`, in Go
  (`ingressnginx.Controller.Service`) and in the generated Python models: the Service is
  fixed in the chart's values. Remove the field from any values that set it.
