### Fixed
- The control-plane operator could be OOMKilled at its 128Mi memory limit on a cluster
  with many Argo CD Applications. The cp chart now gives the manager a 128Mi request and
  a 512Mi limit and sets `GOMEMLIMIT` from that limit, and the operator's cache no longer
  keeps `managedFields`.
