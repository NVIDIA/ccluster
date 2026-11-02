### Changed
- `CCLClusterConfig` `status.loadBalancers` holds at most 256 entries and requires `ip`
  to be a canonical IP address without a zone, and `hostname` a lowercase DNS name that
  is not an IP address. The ip operator writes the IP in canonical form and skips an
  address that does not fit. The cp operator checks them again, since an infra-plane
  cluster's admin can write that status regardless, and leaves an invalid one out of
  `status.clusterConfig` and the DNS records, reporting it as `InvalidLoadBalancer`.
