### Added
- `ccluster dashboard`, from the new `ccluster-py[dashboard]` extra, serves a read-only web
  page for the control plane saved by `ccluster init` on `127.0.0.1`: a namespace dropdown,
  a summary of infra planes, component sets and their components' health, and tables of
  both, refreshed every few seconds. Without `-n` it covers every namespace, which needs
  cluster-wide read access; with `-n` it covers one. It reads with the kubeconfig
  context's credentials, so Kubernetes RBAC decides what it shows.
- `ccluster.client.current_conditions`, the current-generation conditions of a
  CCLInfraPlane or CCLComponentSet, and `ccluster.client.component_set_finished`, the
  check `CCLClient.is_component_set_finished` makes, on a set already read.
