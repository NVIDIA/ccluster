### Fixed
- `ccluster register` and `ccluster unregister` in `ccluster-py` show a one-line error,
  as `ccluster init` does, when the saved control plane does not serve the ccluster API.
  All three commands now say the control plane "does not serve the ccluster API"; `init`
  used to name CCLInfraPlane.
