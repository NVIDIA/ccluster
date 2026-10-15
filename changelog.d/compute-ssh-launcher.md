### Added
- The compute-ssh launcher that `compute_deployment` copies into the user container is
  built from `catalogs/deployments/compute_deployment/compute-ssh` and published as
  `ghcr.io/nvidia/ccluster-compute-ssh`, tagged with the commit hash on every merge to
  main and with `X.Y.Z` on release.

### Changed
- `compute_deployment`'s `sshInjectorImage` is optional and defaults to
  `ghcr.io/nvidia/ccluster-compute-ssh` at the release version. A caller that sets it
  keeps its own image; the image must provide `/compute-ssh`.
