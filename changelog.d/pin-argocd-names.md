### Changed
- **Breaking:** the cp chart pins the bundled Argo CD's names with
  `argo-cd.fullnameOverride: ccluster-cp-argocd`, so its objects are named
  `ccluster-cp-argocd-*` whatever the release name. A release named `ccluster-cp` keeps
  its names. Upgrading a release with another name renames Argo CD's Deployments,
  StatefulSet, Services and ClusterRoles: Helm deletes the old objects and creates new
  ones, so Argo CD is briefly unavailable, and anything that refers to
  `<release>-argocd-server`, such as an Ingress, must be pointed at
  `ccluster-cp-argocd-server`. Secrets and `argocd-cm` keep their names.
