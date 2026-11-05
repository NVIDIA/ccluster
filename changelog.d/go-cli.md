### Added
- A Go build of the `ccluster` command line, released as
  `ccluster_X.Y.Z_<os>_<arch>.tar.gz` for Linux and macOS on amd64 and arm64 and
  installable with `go install github.com/NVIDIA/ccluster/cmd/ccluster@vX.Y.Z`. It has the
  Python command's `init`, `register` and `unregister`, their long flags and the same
  config file; it does not accept the short forms `-cp` and `-ip`. The
  Python command stays in `ccluster-py`; install one or the other, since both are called
  `ccluster`.
- `ccluster completion bash|zsh|fish` in the Go command prints a shell
  completion script; the README shows how to load it.
- `github.com/NVIDIA/ccluster/pkg/cclclient`, a Go client designed like the Python
  `CCLClient`: it registers and unregisters infra planes and applies, polls and deletes
  component sets in one namespace. `ComponentSetFinished` checks a set already in hand,
  such as one from an informer, for convergence. `catalogs.Component` places the entry a
  chart package's `New` returns onto an infra plane.

### Changed
- **Breaking:** The Python `CCLClient`, and so the Python `ccluster` command, applies
  under the field manager `ccluster` instead of `ccluster-py`, the same as the Go client
  and command, so either can update or shrink what the other applied.
