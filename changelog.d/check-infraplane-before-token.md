### Added
- Go: `Client.CheckInfraPlane` in `pkg/cclclient` dry-runs the `CCLInfraPlane`
  `ApplyInfraPlane` writes, so the API server checks it without anything being written.

### Fixed
- In the Go CLI, `ccluster register` has the control plane check the `CCLInfraPlane`,
  its name included, before creating the cluster-admin ServiceAccount, binding and token
  on the infra-plane cluster, so a refused name leaves nothing behind there (#303).
