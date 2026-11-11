### Added
- `ccluster-cp-operator` reads its base domain from `CCLUSTER_BASE_DOMAIN` when
  `--base-domain` is not given, and the `ccluster-cp` chart sets it with
  `--set manager.envOverrides.CCLUSTER_BASE_DOMAIN=<domain>`. Installing no longer means
  replacing the chart's `manager.args` list, so a release keeps its own pinned
  `--catalog-revision`.
