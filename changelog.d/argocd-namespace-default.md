### Changed
- **Breaking:** the cp operator's `--argocd-namespace` defaults to the operator's own
  namespace, where the cp chart installs Argo CD, instead of `argocd`. A plain
  `helm install` of the chart with its bundled Argo CD now works in any namespace. With an
  existing Argo CD in another namespace, set `--argocd-namespace` in `manager.args`, or
  `ARGOCD_NAMESPACE` for `make deploy-cp`.
