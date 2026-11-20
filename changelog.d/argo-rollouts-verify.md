### Added
- The ip operator verifies `argo_rollouts`: `Verified` stays False until the
  CustomResourceDefinition `rollouts.argoproj.io`, the kind inference deployments render,
  exists, and a controller holds the `argo-rollouts-controller-lock` Lease and renewed it
  within its lease duration plus 30 seconds. CRDs left behind by an uninstall therefore
  do not pass. Both are found by name, so an Argo Rollouts installed by other means
  passes too, provided its controller runs leader election. The ip operator's
  ClusterRole now reads CustomResourceDefinitions, of which it caches only metadata, and
  lists Leases, which it reads from the API server by name and never caches.
