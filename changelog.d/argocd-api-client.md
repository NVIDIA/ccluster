### Added
- The cp chart declares `ccluster-cp`, a read-only Argo CD account (`apiKey` only,
  `applications, get` on every Application), for the operator to read Applications'
  resource trees, live manifests and events from argocd-server. When the chart installs
  Argo CD, a post-install hook creates the account's token and stores it in Secret
  `ccluster-cp-argocd-token`; with an existing Argo CD, the user creates it.
