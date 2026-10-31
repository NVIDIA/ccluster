### Added
- The `ccluster` command line is a Go binary, released as
  `ccluster_X.Y.Z_<os>_<arch>.tar.gz` for Linux and macOS on amd64 and arm64 and
  installable with `go install github.com/NVIDIA/ccluster/cmd/ccluster@vX.Y.Z`. It keeps
  the Python command's `init`, `register` and `unregister` and its config file.
- `github.com/NVIDIA/ccluster/pkg/cclclient`, a Go client designed like the Python
  `CCLClient`: it registers and unregisters infra planes and applies, polls and deletes
  component sets in one namespace. `catalogs.Component` builds a component from a chart's
  typed values, like Python's `catalogs.component`.

### Changed
- **Breaking:** `ccluster init` takes `--context` in place of `-cp`/`--control-plane`,
  and `ccluster register` takes `--context` in place of `-ip`/`--infra-plane`.
- **Breaking:** The Python `CCLClient` applies under the field manager `ccluster` instead
  of `ccluster-py`, the same as the Go client and the CLI, so either can update or shrink
  what the other applied.

### Removed
- **Breaking:** `ccluster-py` no longer installs a `ccluster` command, and no longer
  declares `click` or `pyyaml` as dependencies. Install the Go binary instead.
