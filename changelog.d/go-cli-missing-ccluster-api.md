### Fixed
- In the Go CLI, `ccluster register`, `ccluster unregister`, `ccluster list`,
  `ccluster get` and `ccluster server` check that the saved control plane serves the
  ccluster API before using it, and `ccluster init` now says "does not serve the
  ccluster API" rather than naming `CCLInfraPlane`.
