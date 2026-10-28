### Added
- The cp operator watches each infra-plane cluster's `CCLClusterConfig` and copies its
  status, verbatim, into the new `CCLInfraPlane.status.clusterConfig`, so the ip operator's
  `Verified` condition is visible on the control plane. `kubectl get cclinfraplane` shows it
  in a `Verified` column. A change on the infra plane, including a deleted
  `CCLClusterConfig`, reconciles the `CCLInfraPlane` at once; `--infraplane-resync-period`
  becomes a safety net.
- A `ccluster_remote_watches` metric counts the infra-plane clusters being watched.

### Changed
- **Breaking:** The generated Python models reuse identical classes (`datamodel-codegen
  --reuse-model`). In `ccluster.v1alpha1.cclinfraplane`, `Condition` is now the type of
  `status.clusterConfig.conditions`, and `status.conditions` takes `Condition1`, a subclass
  of it. A `CCLInfraPlaneStatus` built from `Condition` instances fails validation; build it
  from `Condition1` instances or dicts. Reading conditions is unchanged.
