### Fixed
- The control-plane operator could be OOMKilled at its 128Mi memory limit on a cluster
  with many Secrets or Argo CD Applications, including ones ccluster does not manage. The
  cp chart now gives the manager a 128Mi request and a 512Mi limit. Both operators now
  set the Go runtime's memory limit to 90% of their container's, unless `GOMEMLIMIT` is
  set, and their caches no longer keep `managedFields`.
