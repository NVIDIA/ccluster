# Build, CI and release contracts

Read this before changing Docker builds, CI workflows or artifact versioning. For the
release procedure use the [release skill](../.agents/skills/release/SKILL.md); for commits
and PRs use [commit-pr](../.agents/skills/commit-pr/SKILL.md). Deployment mechanics are in
[Development](development.md), and test layers are in [Testing](testing.md).

## Images and caching

One Dockerfile builds both binaries, selected by `CMD`. Its build stage runs on
`BUILDPLATFORM` and cross-compiles with `TARGETOS` / `TARGETARCH`, avoiding emulation.
Go module and build caches are BuildKit cache mounts shared by successive builds on the
builder. Do not add `go build -a`, which defeats incremental compilation.

CI retains image layers through `--cache-from/--cache-to type=gha`, passed via
`BUILDX_ARGS`. `.dockerignore` admits Go sources, module files, `LICENSE` and
`THIRD_PARTY_NOTICES.md`, so other documentation changes can reuse the build layer. Do not restore cache mounts through
`reproducible-containers/buildkit-cache-dance`: staging multiple cache copies exhausted
runner disk space. Cache mounts currently persist on the builder, not across CI runs.

Use `make docker-build-cp` / `docker-build-ip` for local images. The corresponding
`docker-buildx-*` targets build and push for `PLATFORMS` (default
`linux/amd64,linux/arm64`); `make docker-buildx` does both. It always pushes because the
multi-architecture manifest cannot be loaded into the local image store.

`REGISTRY` defaults to `ghcr.io/nvidia`. `IMG_CP` and `IMG_IP` name the separate images;
`IMG_TAG` defaults to the 12-character commit hash with `-dirty` for uncommitted changes.
Do not introduce `:latest` or `:dev` tags. Kubernetes defaults `:latest` to pull policy
`Always`, which defeats locally loaded kind images; commit tags use `IfNotPresent` by
default. Reloading the same tag requires a Deployment restart.

## Artifact versions and publishing

| Artifact | Name/location |
| --- | --- |
| Images | `ghcr.io/nvidia/ccluster-{cp,ip}-operator` |
| Helm charts | `oci://ghcr.io/nvidia/charts/ccluster-{cp,ip}` |
| Python | PyPI `ccluster-py`, imported as `ccluster` |
| Catalog | This repository, selected by catalog revision |

The root `VERSION` file is the committed release version. A release PR changes it together
with the changelog; the release workflow stamps artifacts from its tag and
`make check-version` checks that the two agree. Operator `Chart.yaml` versions stay
`0.0.0-dev`, and `python/pyproject.toml` stays `0.0.0.dev0` in the committed tree.

- Every merge to `main` runs `main.yml`, publishing images as `:<sha>` and charts as
  `<next>-dev.<sha>`, where `next` is the committed version with its patch incremented.
  The packaged chart points at that image hash and catalog commit.
- Publishing a GitHub Release `vX.Y.Z` runs `release.yml`. It retags the images already
  built for the release's merge commit, packages charts as `X.Y.Z` pointing at those
  images and catalog tag `vX.Y.Z`, publishes Python through the `pypi` trusted-publishing
  environment, and attaches chart and Python archives to the release.
- Never rebuild images for a release or add a second `push: tags:` trigger. A bare pushed
  tag does not publish a release. A commit without published hash-tagged images cannot
  be released through the retag workflow.

Without an explicit Make variable `VERSION`, packaging uses the development chart version,
image hash and catalog commit. With `VERSION=X.Y.Z`, it stamps release versions and the
catalog tag. `docker-retag-*` requires an explicit version. Fork workflows pass their own
lowercase `REGISTRY`, as GHCR namespaces must be lowercase.

`make python-build` changes `python/pyproject.toml` through `uv version`. Restore only that
build-induced version change before committing; preserve unrelated edits. An `X.Y.Z-rc.N`
tag becomes Python version `X.Y.ZrcN`. The GitHub prerelease flag does not change stamping.

## License payload

Every published artifact carries its license; keep it that way when changing a recipe.

| Artifact | Contents | Added by |
| --- | --- | --- |
| Images | `/licenses/LICENSE`, `/licenses/THIRD_PARTY_NOTICES.md`, and the license and NOTICE files of every linked Go module and the Go standard library under `/licenses/go` | `Dockerfile` (`licenses` stage, `go-licenses`) |
| Helm charts | `LICENSE` and `THIRD_PARTY_NOTICES.md` at the chart root | `helm-package-cp` / `helm-package-ip` |
| Python | `LICENSE` in the sdist and in the wheel's `dist-info/licenses` | `python/LICENSE`, `license-files` in `pyproject.toml` |

`go-licenses` fails the image build when a newly linked module's license cannot be
classified; resolve it rather than ignoring the module. `python/LICENSE` is a copy of the
root `LICENSE` because the package builds from `python/`. Update `THIRD_PARTY_NOTICES.md`
when a bundled chart (the cp chart's `argo-cd`) or a catalog chart dependency changes.

## CI checks

The workflow files are the source of truth for exact path filters. Keep filters aligned
with each job's inputs when changing build or generation dependencies.

- `lint` covers Go and linter inputs; `test` and `test-e2e` cover Go, build, deployment
  and test inputs.
- `test-python` covers Python and schema-generator inputs, including API/catalog Go
  comments. `test-chart` checks generated chart drift and rendering.
- The filtered `lint`, `test`, `test-e2e` and `test-python` jobs include `VERSION`, so a
  release PR runs them all. Its `release` label selects full e2e.
- A skipped filtered workflow is expected for unrelated changes. Do not make these
  filtered checks required in branch protection: a skipped workflow can prevent merging.
- `dco` and `pr-title` enforce sign-off and the Conventional Commit PR title. The repository
  squash-merges the PR title and body into commit history; follow the commit skill's format.
