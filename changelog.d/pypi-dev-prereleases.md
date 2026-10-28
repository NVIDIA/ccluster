### Added
- Every merge to `main` publishes `ccluster-py` to PyPI as a pre-release of the next
  version, `X.Y.Z.dev<N>` with `N` the commit count, next to the `X.Y.Z-dev.<sha>` charts
  of the same commit, so a development chart has a matching client. `pip` installs it only
  when pinned exactly or given `--pre`.
