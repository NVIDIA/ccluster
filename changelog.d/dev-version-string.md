### Changed
- Charts, images and `ccluster-py` built from `main` share one version, `X.Y.Z-dev.<N>`
  with `N` the commit count (`X.Y.Z.dev<N>` for pip), so one string pins a development
  chart and its client, and Helm and pip order development builds by commit. After a
  release candidate `X.Y.Z-rc.K` the version is `X.Y.Z-rc.K.post.<N>`. Images keep
  `:<sha>` and are also tagged with the version. Charts published before as
  `X.Y.Z-dev.<sha>` sort above the new ones, so pin a development chart by exact
  `--version` until the next release.
