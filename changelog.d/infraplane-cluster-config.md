### Added
- The cp operator writes the `CCLClusterConfig` singleton into each infra-plane cluster,
  carrying the `CCLInfraPlane`'s domain, using the plane's credentials Secret (the one Argo
  CD uses). A new `ClusterConfigApplied` condition reports `Applied`, `CRDMissing`,
  `Conflict`, `AccessDenied`, `ClusterUnreachable`, `UnsupportedCredentials` or
  `InvalidCredentials`, and `Unknown/NotAvailable` while `Available` is `False`. A plane
  writes and deletes only the `CCLClusterConfig` it created: any other, such as one applied
  by hand, is left alone and reported as `Conflict`, retried
  every minute, until it is deleted. Deleting the `CCLInfraPlane` deletes the
  `CCLClusterConfig` it created. A plane registering the cp cluster itself is written with the
  operator's own ServiceAccount, which the cp role now grants `cclclusterconfigs`.
- New cp flag `--infraplane-resync-period` (default 5m): how often a written
  `CCLClusterConfig` is rewritten to restore drift.

### Changed
- **Breaking:** a `CCLClusterConfig` applied by hand to a cluster a `CCLInfraPlane`
  registers makes that plane report `Conflict`. Delete it so the cp operator writes its
  own.
- **Breaking:** `CCLInfraPlane.spec.domain` is required and immutable. `ccluster register`
  takes a required `--domain`, and `CCLClient.apply_infraplane` a required `domain`.
