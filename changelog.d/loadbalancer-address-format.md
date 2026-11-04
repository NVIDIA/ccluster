### Changed
- `CCLClusterConfig` `status.loadBalancers` requires `hostname` to be a lowercase DNS
  name. The ip operator writes `ip` as a canonical IP address without a zone, and skips
  an address that does not fit that form or a hostname that is an IP address. The cp
  operator checks them again, since an infra-plane cluster's admin can write that status
  regardless, and leaves an invalid one out of `status.clusterConfig` and the DNS
  records, reporting it as `InvalidLoadBalancer`.
