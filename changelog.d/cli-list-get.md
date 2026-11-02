### Added
- `ccluster list infraplanes|componentsets -n NS` and
  `ccluster get infraplane|componentset NAME -n NS` show what is registered and deployed in
  a namespace, as a table, a summary, or with `-o yaml|json` the objects. `list -A` lists
  across every namespace.
- `Client.ListInfraPlanes` and `Client.ListComponentSets` in Go, and
  `CCLClient.list_infraplanes` and `CCLClient.list_component_sets` in Python, list one
  namespace.

### Fixed
- `cclclient.NewForConfig` returns an error, and Python's `CCLClient` raises `ValueError`,
  for an empty namespace, which the Kubernetes clients read as every namespace.
