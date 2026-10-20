### Added
- The cp chart declares `ccluster-cp`, a read-only Argo CD account (`apiKey` only,
  `applications, get` on every Application), for the operator to read Applications'
  resource trees, live manifests and events from argocd-server. When the chart installs
  Argo CD, a post-install hook creates the account's token and stores it in Secret
  `ccluster-cp-argocd-token`; with an existing Argo CD, the user creates it.
- The cp operator reads argocd-server's API through new flags: `--argocd-server-url`,
  `--argocd-token-secret`, and `--argocd-server-ca-secret` or
  `--argocd-server-insecure-skip-tls-verify` for its certificate. `make deploy-cp` sets them
  for the bundled Argo CD.
