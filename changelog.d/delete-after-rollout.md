### Added
- The cp operator prunes a component's ExternalSecrets, ConfigMaps and
  ResourceClaimTemplates that its chart no longer renders, once the component has finished
  rolling out, by deleting them through argocd-server. Such an object is one the chart
  keeps with `Prune=false` and `IgnoreExtraneous`, which Argo CD marks `requiresPruning`.

### Changed
- Component health leaves out the objects a chart no longer renders.
- **Breaking:** the cp chart's Argo CD account `ccluster-cp` may also delete
  ExternalSecrets, ConfigMaps and ResourceClaimTemplates of any Application. With an
  existing Argo CD, add the three `delete/...` lines of the cp chart's
  `argo-cd.configs.rbac` to the account's policy; without them those objects are not
  pruned, and the operator logs the 403.
