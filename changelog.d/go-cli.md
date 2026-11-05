### Added
- A Go build of the `ccluster` command line, released as
  `ccluster_X.Y.Z_<os>_<arch>.tar.gz` for Linux and macOS on amd64 and arm64 and
  installable with `go install github.com/NVIDIA/ccluster/cmd/ccluster@vX.Y.Z`. It has the
  Python command's `init`, `register` and `unregister` and reads the same config file,
  taking `--context` in place of `-cp`/`--control-plane` and `-ip`/`--infra-plane`. The
  Python command stays in `ccluster-py`; install one or the other, since both are called
  `ccluster`.
- `ccluster completion bash|zsh|fish|powershell` in the Go command prints a shell
  completion script; the README shows how to load it.
- `github.com/NVIDIA/ccluster/pkg/cclclient`, a Go client designed like the Python
  `CCLClient`: it registers and unregisters infra planes and applies, polls and deletes
  component sets in one namespace. `catalogs.Component` places the entry a chart package's
  `New` returns onto an infra plane.

### Changed
- **Breaking:** The Python `CCLClient`, and so the Python `ccluster` command, applies
  under the field manager `ccluster` instead of `ccluster-py`, the same as the Go client
  and command, so either can update or shrink what the other applied.
