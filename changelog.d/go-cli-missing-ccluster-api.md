### Fixed
- `ccluster` `register`, `unregister`, `list` and `get` fail with the same one-line error as
  `init` when the saved control plane does not serve the ccluster API. Before, `unregister`
  reported success, and `register` created the cluster-admin ServiceAccount and token on the
  infra plane before it failed with a generic 404.
