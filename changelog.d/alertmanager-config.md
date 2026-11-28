### Fixed
- `catalogs/cluster_base/kube_prometheus_stack` ships `alertmanager.config` with the
  no-op `null` receiver instead of an empty `receivers` list, so the Alertmanager
  StatefulSet reconciles on a fresh install.
