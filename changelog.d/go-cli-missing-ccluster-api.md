### Fixed
- `ccluster register`, `ccluster unregister`, `ccluster list` and `ccluster get` check
  that the saved control plane serves the ccluster API before using it, and `ccluster init`
  now says "does not serve the ccluster API" rather than naming `CCLInfraPlane`.
