### Added
- `CCLComponentSet` reports `status.componentStatuses`: each component's `state`
  (`Pending`, `Starting`, `Running`, `Failing`, `Succeeded`, `Failed` or `Unknown`),
  `reason`, `message` and `lastObservedTime`, as its chart's status function decides from
  the component's objects, live manifests and events read through argocd-server. Every
  chart reports `Unknown/StatusNotImplemented` until it has a status function of its own.
