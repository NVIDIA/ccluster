### Added
- The cp chart declares `ccluster-cp`, a read-only Argo CD account (`apiKey` only,
  `applications, get` on every Application), for the operator to read Applications'
  resource trees, live manifests and events from argocd-server. `make argocd-token-cp`
  generates its token on a development cluster and stores it in Secret
  `ccluster-cp-argocd-token`.
