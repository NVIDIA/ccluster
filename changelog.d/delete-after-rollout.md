### Added
- The cp operator deletes a component's old ExternalSecrets, ConfigMaps and
  ResourceClaimTemplates once the component has finished rolling out, through
  argocd-server. An old object is one the chart no longer renders and keeps with
  `Prune=false` and `IgnoreExtraneous`.

### Changed
- Component health leaves out the objects a chart no longer renders.
- **Breaking:** the cp chart's Argo CD account `ccluster-cp` may also delete
  ExternalSecrets, ConfigMaps and ResourceClaimTemplates of any Application. With an
  existing Argo CD, add the three `delete/...` lines of the cp chart's
  `argo-cd.configs.rbac` to the account's policy; without them old objects stay, and the
  operator logs the 403.
