### Added
- `CCLInfraPlane` reports a `Ready` condition summarizing the others: `True` once the
  cluster is registered with Argo CD, carries the plane's domain and is verified by the
  infra-plane operator, otherwise `False` with a reason naming the first of those that is
  not (`NotAvailable`, `ClusterConfigNotApplied`, `NotVerified`), or `Deleting` while the
  plane is being deleted. `DNSReady` does not count, and nothing waits for `Ready`.
  `ccluster list`, the dashboard and `kubectl wait --for=condition=Ready` show it.

### Changed
- `kubectl get cclinfraplanes` shows `Ready`, `Domain` and `DNS`; `Available`,
  `ClusterConfig` and `Verified` move to `-o wide`. `ccluster list infraplanes` prints
  the same default columns.
