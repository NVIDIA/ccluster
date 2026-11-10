### Added
- `ccluster server` serves a read-only dashboard of the control plane saved by
  `ccluster init` at `http://127.0.0.1:8484` and opens it in the browser. It summarises
  infra planes by availability and verification, component sets by status (finished,
  rolling out, failed, terminating) and their components by health, and tables both
  kinds, refreshing every five seconds and keeping the last data with a notice when a
  refresh fails. Without `-n` it reads across the cluster and offers every namespace
  holding either kind; without cluster-wide read access it says so and suggests `-n`.
  With `-n` it reads only that namespace. It reads with the kubeconfig context's
  credentials, listens only on a loopback address, and answers only requests addressed
  to a loopback host name. The React UI is embedded in the binary, so the release
  tarballs and `go install` include it.
