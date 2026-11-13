### Added
- The ip operator verifies `argo_rollouts`: `Verified` stays False until the
  CustomResourceDefinition `rollouts.argoproj.io`, the kind inference deployments render,
  exists. It checks the CRD only, not that a controller reconciles Rollouts, so an Argo
  Rollouts installed by other means passes too. The ip operator's ClusterRole now reads
  CustomResourceDefinitions, and it caches only their metadata.
