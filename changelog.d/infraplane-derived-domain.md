### Added
- `CCLClusterConfig` gains `status.domain`: the `spec.domain` the ip operator last verified
  the cluster against, from the same pass as `observedGeneration`. The cp operator copies
  it into the plane's `status.clusterConfig.domain`, which a new `Domain` column on
  `CCLInfraPlane` shows.

### Changed
- **Breaking:** The cp operator requires `--base-domain` and refuses to start without a
  valid one. Each CCLInfraPlane's domain is derived as `<name>.<namespace>.<base-domain>`.
  The cp operator writes that domain to the plane's `CCLClusterConfig` and publishes
  `*.<lb>.<name>.<namespace>.<base-domain>` for each labelled LoadBalancer. Two planes can
  never claim equal or overlapping domains (#159), and one DNS zone for the base domain
  covers every plane. Changing the base domain moves every plane's domain, records and
  SPIFFE trust domain, so set it once per control plane. Add `--base-domain` to the cp
  chart's `manager.args`. `make deploy-cp` and `make run-cp` pass `BASE_DOMAIN`, which
  defaults to `example.com`.
- **Breaking:** A CCLInfraPlane's name must be a DNS label of at most 63 characters, since
  the domain is derived from it.
