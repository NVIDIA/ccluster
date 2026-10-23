### Added
- The cp operator writes the `CCLClusterConfig` singleton into each infra-plane cluster,
  carrying the `CCLInfraPlane`'s domain, using the plane's credentials Secret (the one Argo
  CD uses). A new `ClusterConfigApplied` condition reports `Applied`, `CRDMissing`,
  `Conflict`, `ClusterUnreachable`, `UnsupportedCredentials`, `InvalidCredentials` or
  `DomainMissing`. Deleting the `CCLInfraPlane` deletes the `CCLClusterConfig` if the
  operator wrote it. A plane registering the cp cluster itself is written with the
  operator's own ServiceAccount, which the cp role now grants `cclclusterconfigs`.
- New cp flag `--infraplane-resync-period` (default 5m): how often a written
  `CCLClusterConfig` is rewritten to restore drift.

### Changed
- **Breaking:** `CCLInfraPlane.spec.domain` is required and immutable. `ccluster register`
  takes a required `--domain`, and `CCLClient.apply_infraplane` a required `domain`.
