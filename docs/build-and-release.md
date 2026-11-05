# Build, CI and release contracts

Read this before changing Docker builds, CI workflows, the Go version or artifact
versioning. For the release procedure use the
[release skill](../.agents/skills/release/SKILL.md); for commits and PRs use
[commit-pr](../.agents/skills/commit-pr/SKILL.md). Deployment mechanics are in
[Development](development.md), and test layers are in [Testing](testing.md).

## Go version

The `toolchain` line in `go.mod` is the one Go version the repository builds with; the
`go` line above it is only the minimum for modules that import this one, such as users of
`pkg/client/`. Each build path follows the `toolchain` line:

| Where | How |
| --- | --- |
| CI workflows | `setup-go` with `go-version-file: go.mod` installs the `toolchain` version. |
| Local `make` targets | The Makefile exports `GOTOOLCHAIN` from `go.mod`, so every `go` command a target runs, including tool installs, uses that version whatever Go is installed. The go command downloads it on first use. |
| Tools in `bin/` | Each Go tool is built into `bin/<tool>@<version>-<toolchain>/`, so a tool or toolchain change names a missing binary and `make` builds it. |
| Images | The Dockerfile's `golang:` builder tag. `make check-go-version`, run by the `lint` workflow, fails when it differs from the `toolchain` line. |

Running `go` directly outside `make` uses the installed Go when it is newer than the
`toolchain` line, so generate code and run checks through `make`.

To change the version, edit the `toolchain` line (`go mod edit -toolchain=goX.Y.Z`) and
the Dockerfile's `golang:X.Y.Z` tag in one PR, then rerun the generators (`make manifests
generate client-generate helm-generate python-generate`) and commit any changes they make.
Dependabot's weekly `golang` image bump changes only the Dockerfile, so it fails
`check-go-version` until the same PR updates `go.mod`. A new Go minor release can need a
newer golangci-lint, whose type checker must read that release's export data; Go 1.27.2
needs v2.14.0 or later. Its version lives in `.custom-gcl.yml` alone, which the
Makefile reads for the plain install too. Raise the `go` line only when the code needs a
newer language version or standard library.

## Images and caching

One Dockerfile builds every binary in this repository, selected by `PKG`, the Go package
path (`./cmd/ccluster-cp-operator`, `./catalogs/deployments/compute_deployment/compute-ssh`).
The last stage is the operator image; the compute-ssh launcher image is the `compute-ssh`
target (`--target compute-ssh`), and `compute_deployment`'s init container depends on its
`/compute-ssh` path. The build stage runs on `BUILDPLATFORM`
and cross-compiles with `TARGETOS` / `TARGETARCH`, avoiding emulation.
Go module and build caches are BuildKit cache mounts shared by successive builds on the
builder. Do not add `go build -a`, which defeats incremental compilation.

The runtime base image carries its own OCI labels, so both runtime stages overwrite every
label it sets; an inherited label cannot be removed. The Makefile passes `REVISION` (the
commit) for `org.opencontainers.image.revision`. `version` and `created` stay empty: a
release retags the image built for its commit, so a baked-in version would be wrong, and a
build timestamp would make builds of one commit differ. Build images through the Makefile
targets so `REVISION` is set.

CI retains image layers through `--cache-from/--cache-to type=gha`, passed via
`BUILDX_ARGS`. `.dockerignore` admits Go sources, module files, `LICENSE` and
`THIRD_PARTY_NOTICES.md`, so other documentation changes can reuse the build layer. Do not restore cache mounts through
`reproducible-containers/buildkit-cache-dance`: staging multiple cache copies exhausted
runner disk space. Cache mounts currently persist on the builder, not across CI runs.

Use `make docker-build-cp` / `docker-build-ip` / `docker-build-compute-ssh` for local
images. The corresponding `docker-buildx-*` targets build and push for `PLATFORMS`
(default `linux/amd64,linux/arm64`); `make docker-buildx` does all three. It always pushes
because the multi-architecture manifest cannot be loaded into the local image store.

`REGISTRY` defaults to `ghcr.io/nvidia`. `IMG_CP`, `IMG_IP` and `IMG_COMPUTE_SSH` name the
separate images; `IMG_TAG` defaults to the 12-character commit hash with `-dirty` for uncommitted changes.
Do not introduce `:latest` or `:dev` tags. Kubernetes defaults `:latest` to pull policy
`Always`, which defeats locally loaded kind images; commit tags use `IfNotPresent` by
default. Reloading the same tag requires a Deployment restart.

## Artifact versions and publishing

| Artifact | Name/location |
| --- | --- |
| Images | `ghcr.io/nvidia/ccluster-{cp,ip}-operator`, `ghcr.io/nvidia/ccluster-compute-ssh` |
| Helm charts | `oci://ghcr.io/nvidia/charts/ccluster-{cp,ip}` |
| Python | PyPI `ccluster-py`, imported as `ccluster` |
| CLI | `ccluster_<version>_<os>_<arch>.tar.gz` on the GitHub Release (`make cli-package`, linux and darwin on amd64 and arm64); `go install github.com/NVIDIA/ccluster/cmd/ccluster@vX.Y.Z` |
| Catalog | This repository, selected by catalog revision |

The root `VERSION` file is the committed release version. A release PR changes it together
with the changelog; the release workflow stamps artifacts from its tag and
`make check-version` checks that the two agree. Operator `Chart.yaml` versions stay
`0.0.0-dev`, and `python/pyproject.toml` stays `0.0.0.dev0` in the committed tree.
The exception is `compute_deployment`'s default `sshInjectorImage`: catalogs are read
from git, so the release PR writes the launcher tag into its `values.yaml` next to
`VERSION`, and a test fails when the two differ.

- Every merge to `main` runs `main.yml`, publishing all three images as `:<sha>` and charts as
  `<next>-dev.<sha>`, where `next` is the committed version with its patch incremented.
  The packaged chart points at that image hash and catalog commit. The same run publishes
  `ccluster-py==<next>.dev<N>` to PyPI, where `N` is `git rev-list --count <sha>`: PEP 440
  has no room for a hash and PyPI rejects local versions. `main` is squash-merged and
  linear, so `N` grows with every merge and names one commit. The job checks out the full
  history, and `make python-build` refuses a shallow clone for a dev build. The PyPI file's
  provenance and the run summary record the commit.
- Publishing a GitHub Release `vX.Y.Z` runs `release.yml`. It retags the images already
  built for the release's merge commit, packages charts as `X.Y.Z` pointing at those
  images and catalog tag `vX.Y.Z`, publishes Python through the `pypi` trusted-publishing
  environment, and attaches chart, CLI and Python archives to the release. The CLI is
  built only for releases; `main.yml` publishes no dev binaries. PyPI trusts both
  `main.yml` and `release.yml` in that environment. In both workflows one job builds the
  Python dists and a second job, the only one with `id-token: write`, uploads them: every
  step of a job can fetch its OIDC token, so build and repository code never runs there.
- Never rebuild images for a release or add a second `push: tags:` trigger. A bare pushed
  tag does not publish a release. A commit without published hash-tagged images cannot
  be released through the retag workflow.

Without an explicit Make variable `VERSION`, packaging uses the development chart, CLI and
Python versions, image hash and catalog commit. With `VERSION=X.Y.Z`, it stamps release versions and the
catalog tag. `docker-retag-*` requires an explicit version. Fork workflows pass their own
lowercase `REGISTRY`, as GHCR namespaces must be lowercase.

`make python-build` changes `python/pyproject.toml` through `uv version`. Restore only that
build-induced version change before committing; preserve unrelated edits. An `X.Y.Z-rc.N`
tag becomes Python version `X.Y.ZrcN`. The GitHub prerelease flag does not change stamping.

## License payload

Every published artifact carries its license; keep it that way when changing a recipe.

| Artifact | Contents | Added by |
| --- | --- | --- |
| Images | `/licenses/LICENSE`, `/licenses/THIRD_PARTY_NOTICES.md`, and the license and NOTICE files of every linked Go module and the Go standard library under `/licenses/go` | `Dockerfile` (`go-licenses` in the builder stage) |
| Helm charts | `LICENSE` and `THIRD_PARTY_NOTICES.md` at the chart root | `helm-package-cp` / `helm-package-ip` |
| Python | `LICENSE` in the sdist and in the wheel's `dist-info/licenses` | `python/LICENSE`, `license-files` in `pyproject.toml` |
| CLI tarballs | `LICENSE`, `THIRD_PARTY_NOTICES.md`, and under `licenses/go` the license and NOTICE files of every linked Go module and the Go standard library | `cli-package` (`go-licenses`, at the Dockerfile's `GO_LICENSES_VERSION`) |

`go-licenses` fails the image build and `cli-package` when a newly linked module's
license cannot be classified; resolve it rather than ignoring the module. `python/LICENSE` is a symlink to
the root `LICENSE` because the package builds from `python/`; the build stores the
file's contents, not the link. Update `THIRD_PARTY_NOTICES.md`
when a bundled chart (the cp chart's `argo-cd`) or a catalog chart dependency changes.

## CI checks

The workflow files are the source of truth for exact path filters. Keep filters aligned
with each job's inputs when changing build or generation dependencies.

- `lint` covers Go and linter inputs and the Dockerfile, and runs `make check-go-version`;
  `test` and `test-e2e` cover Go, build, deployment and test inputs. `test-e2e` builds
  every image: the suite builds the operators and a workflow step builds the compute-ssh
  launcher, so a broken Dockerfile stage fails the PR rather than `main.yml`. It also
  covers `catalogs/cluster_base/spire_crds/`, because the suite installs that chart and
  asserts the CRDs it ships. The suite registers its infra-plane cluster with the
  `ccluster` CLI it builds from `cmd/ccluster`, which the Go paths already cover.
- `test-e2e` uses the kind and node image the Makefile pins, `KIND_VERSION` and
  `KIND_NODE_IMAGE`, as local runs do; the workflow only raises the runner's inotify
  limits first. Dependabot updates neither: bump both together, taking the image and its
  digest from the kind release's notes.
- `test` also covers `catalogs/**`, because catalog tests read `Chart.yaml`, values and
  templates. Before the suite it fails when `go.mod` and `go.sum` are not tidy, when
  `make client-generate` changes `pkg/client/`, or when `make manifests generate` changes
  anything in the tree. `make test` regenerates CRDs, RBAC and deepcopy code itself, so
  without the last check a stale CRD, RBAC role or `zz_generated.deepcopy.go` passes CI.
- `test-python` covers Python and schema-generator inputs, including API/catalog Go
  comments. `test-chart` checks generated chart drift and rendering.
- `test-chart` also runs `make helm-lint-catalogs`, which lints and renders every chart
  under `catalogs/` on a copy of the tree. A chart renders once per `ci/*-values.yaml`
  file in its directory, or once with its defaults when it has none. `cluster_base`
  charts fetch their pinned upstream dependencies first, so the step needs network.
  `TestCIValuesAreValid` in `catalogs/components` decodes each `ci/` file strictly into
  the chart's `Values` and runs `Validate`, because a render does not check either.
- The filtered `lint`, `test`, `test-e2e` and `test-python` jobs include `VERSION`, so a
  release PR runs them all. Its `release` label selects full e2e.
- A skipped filtered workflow is expected for unrelated changes. Do not make these
  filtered checks required in branch protection: a skipped workflow can prevent merging.
- Dependabot opens weekly version-update PRs for the Go module (`k8s.io/*` and
  controller-runtime in one group), the Python package, the Dockerfile base images and
  GitHub Actions. Their titles use the `build` or `ci` type so `pr-title` accepts them.
  A `golang` image bump needs a commit that updates `go.mod` to match; see
  [Go version](#go-version).
- `dco` and `pr-title` enforce sign-off and the Conventional Commit PR title. The repository
  squash-merges the PR title and body into commit history; follow the commit skill's format.
