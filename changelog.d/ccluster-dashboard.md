### Added
- `ccluster dashboard`, from the new `ccluster-py[dashboard]` extra, serves a read-only web
  view of a namespace of the control plane saved by `ccluster init` on `127.0.0.1`: its
  CCLInfraPlanes and CCLComponentSets, refreshed every few seconds, with each set's
  components in rollout order and the conditions of its current generation. It reads with
  the kubeconfig context's credentials, so Kubernetes RBAC decides what it shows.
- `ccluster.client.current_conditions`, the current-generation conditions of a
  CCLInfraPlane or CCLComponentSet, and `ccluster.client.component_set_finished`, the
  check `CCLClient.is_component_set_finished` makes, on a set already read.
