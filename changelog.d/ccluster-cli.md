### Added
- `ccluster-py` installs a `ccluster` command built on click. `ccluster init -cp CONTEXT`
  saves the control plane's kubeconfig context after checking it serves the ccluster API.
  `ccluster register NAME -n NS -ip CONTEXT [--server URL]` then creates a cluster-admin
  ServiceAccount token on the infra-plane cluster and registers it with that control plane as
  the CCLInfraPlane NAME.
  `ccluster unregister NAME -n NS` deletes the CCLInfraPlane and its credentials Secret,
  leaving the infra plane's ServiceAccount and token in place.
