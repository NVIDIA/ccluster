### Changed
- `ccluster-cp-operator` reads each of its own flags from a `CCLUSTER_*`
  environment variable named after it, such as `CCLUSTER_BASE_DOMAIN` for `--base-domain`,
  with a flag on the command line taking precedence. The `ccluster-cp` chart sets them in
  `manager.envOverrides`, a map, so an install sets the base domain with
  `--set manager.envOverrides.CCLUSTER_BASE_DOMAIN=<domain>` without restating the
  chart's other settings, and a release keeps its own pinned catalog revision.
  `manager.args` now holds only `--leader-elect`; a values file that set the catalog or
  Argo CD flags in `manager.args` still works, as flags win.
