### Removed
- **Breaking:** `namespace_base` no longer creates the `ecr-image-pull` Secret or labels
  the namespace `ecr-anywhere.centml.ai/namespace: enabled`, and the workload charts no
  longer list `ecr-image-pull` in `imagePullSecrets`. A pod now lists only the Secret
  `imagePullSecret` creates and the `imagePullSecretNames`, and has no `imagePullSecrets`
  when both are unset. Private images are pulled through those values or through
  credentials the cluster's nodes already have.
