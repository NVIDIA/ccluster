### Added
- `inference_deployment`, `compute_deployment` and `job_deployment` take `externalSecrets`:
  the External Secrets Operator creates the container's variables and the image pull
  credential from references into a secret store (`secretStoreRef`, `env`, `imagePull`),
  so secret values never enter the values, the CCLComponentSet or the Argo CD
  Application. The chart names each Secret from its references, so a new reference or
  `version` rolls the pods onto a new Secret. The infra plane needs ESO serving
  `external-secrets.io/v1` and the store.
- `catalogs/library/workload`, a Helm library chart the workload charts share
  through a `file://` dependency and a `charts/` symlink.

### Changed
- `inference_deployment`'s per-revision config ConfigMap and `dynamo_deployment`'s EFA
  ResourceClaimTemplate are kept until the rollout that replaces them ends, and the cp
  operator deletes them then, instead of being pruned at the start of the sync while old
  pods may still read them.
