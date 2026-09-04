---
name: release
description: Cut a ccluster release end to end, except approving the release PR. Use when the user asks to release, cut a version, bump versions, prepare or finish a release, or tag a release. Detects where the release stands (no branch, release PR open, PR merged, tag created) and continues from there, so it can be run once before the PR is approved and once after.
---

# Release

A release is one reviewed pull request followed by one GitHub Release, and this skill does
all of it except the approval:

1. **Phase 1, prepare.** Cut `release/vX.Y.Z` from `main`, agree the version with the
   user, consolidate the `Unreleased` changelog entries under `X.Y.Z`, bump the committed
   versions, commit, and open the release PR.
2. **Approval.** Another maintainer reviews and merges the PR. The skill never merges it.
3. **Phase 2, publish.** Create the GitHub Release `vX.Y.Z` on the merge commit with
   `gh`. That creates the tag and fires `.github/workflows/release.yml`, which retags the
   `:<sha>` images, re-packages the charts, publishes `ccluster-py==X.Y.Z` to PyPI and
   attaches the assets. Verify the artifacts.

Nobody has to touch the GitHub UI. Nothing is ever rebuilt for a release: the images a
release ships are the ones `main.yml` pushed for the merge commit, and the workflow
stamps the version from the tag (`VERSION=${tag#v}`). The committed versions and the
changelog exist so the tree and the artifacts agree, and the tag is created only on the
commit that carries them.

## Step 0. Find out where the release stands

Always start here, on a clean checkout with `gh auth status` working:

```bash
git checkout main && git pull --ff-only && git status --short
LAST=$(gh release list --exclude-drafts --limit 20 --json tagName,isPrerelease \
         --jq '[.[] | select(.isPrerelease == false)][0].tagName')   # empty before the first release
gh pr list --state all --search 'in:title "chore(release):"' --limit 5 \
  --json number,title,state,headRefName,mergeCommit
```

Then branch on what you find:

| State | Go to |
| --- | --- |
| No open or recently merged release PR | Phase 1 |
| Release PR open, not merged | Tell the user it awaits approval, give the URL, and offer to wait (Step 6). Do not merge. |
| Release PR merged, no `vX.Y.Z` tag yet | Phase 2 |
| Tag and release exist | Step 8, verify, then report |

The version in the PR title (`chore(release): prepare vX.Y.Z`) is the version for the later
phases. The word "prepare" is not decoration: `pr-title.yml` rejects a summary shorter than
ten characters, and a bare `vX.Y.Z` fails that check.

## Phase 1. Prepare the release PR

### Step 1. Agree the version

Suggest a version, then ask the user to confirm or override it. This is the one question
the skill asks.

- Latest release: `LAST` from Step 0 (`v0.0.0` if none; the first release is then `v0.1.0`
  unless the user says otherwise).
- Read `## [Unreleased]` in `CHANGELOG.md`. Following Semantic Versioning on the `0.y.z`
  line: any `**Breaking:**` entry or `Removed` section, or anything under `Added` or
  `Changed`, suggests a minor bump; only `Fixed`, `Security` or `Deprecated` suggests a
  patch bump.
- Cross-check against the merged PR titles since the last release
  (`git log --format='%s' ${LAST:+$LAST..}main`) so a `feat` or `!` title that never got a
  changelog entry is not missed; it needs one, see Step 3.
- PyPI uploads are permanent. Offer `vX.Y.Z-rc.1` as the alternative when the release is
  the first one or carries breaking changes; uv normalises it to `X.Y.ZrcN`.

Present the suggestion with the one-line reason and the alternative, and wait for the
answer. Everything that follows uses the confirmed version.

### Step 2. Release branch

```bash
git checkout -b release/vX.Y.Z
```

### Step 3. Consolidate the changelog

`CHANGELOG.md` follows Keep a Changelog. Between releases, PRs only **append** entries
under `## [Unreleased]`; the release is the one point where that list is edited.

- Read the full `Unreleased` section.
- Add an entry for any user-visible merged PR since the last release that has none, from
  its title and body. Do not invent detail that is not in them.
- Consolidate: merge entries that describe the same feature from different angles, drop
  entries superseded within the same cycle (a thing added and then renamed ships once,
  under its final name), and keep one bullet per user-facing change. Keep the
  `**Breaking:**` markers. Preserve the subsection order Added, Changed, Deprecated,
  Removed, Fixed, Security and delete empty subsections.
- Rename the heading to `## [X.Y.Z] - YYYY-MM-DD` (today, UTC) and insert a fresh, empty
  `## [Unreleased]` above it.
- Maintain the comparison links at the bottom of the file:

```
[Unreleased]: https://github.com/NVIDIA/ccluster/compare/vX.Y.Z...HEAD
[X.Y.Z]: https://github.com/NVIDIA/ccluster/compare/vPREV...vX.Y.Z
```

For the first release the second link is
`https://github.com/NVIDIA/ccluster/releases/tag/vX.Y.Z`.

For a release candidate the heading is `## [X.Y.Z-rc.N] - date`. When the final release
follows, fold every rc section since the last final release into the single `X.Y.Z`
section and drop the rc headings and links; the rc tags remain on GitHub.

Do not edit sections of already released versions.

### Step 4. Bump the committed versions

These are the only files that carry a version. Set all of them to the same value:

| File | Field | Value |
| --- | --- | --- |
| `charts/cp/chart/Chart.yaml` | `version`, `appVersion` | `X.Y.Z` (or `X.Y.Z-rc.N`) |
| `charts/ip/chart/Chart.yaml` | `version`, `appVersion` | same |
| `python/pyproject.toml` | `version` | `X.Y.Z` (or `X.Y.ZrcN`, PEP 440) |
| `python/uv.lock` | `version` of the `ccluster-py` package entry | same as `pyproject.toml`; `make python-test` rewrites it for you |

Do not touch `manager.image.tag` in the chart values, anything under `dist/`, or
`charts/*/chart/templates/`. Then check the tree still lints and tests:

```bash
make helm-lint
make python-test
git status --short   # only CHANGELOG.md and the four files above may be modified
```

Do not run `make python-build` or `make helm-package` here: `python-build` rewrites
`pyproject.toml` to whatever `VERSION` is, undoing the bump.

### Step 5. Commit and open the release PR

Follow the `commit-pr` skill for format. Title and body:

```
chore(release): prepare vX.Y.Z

Prepare the vX.Y.Z release.

- CHANGELOG.md consolidates the Unreleased entries under X.Y.Z dated
  YYYY-MM-DD and opens a new empty Unreleased section.
- charts/cp/chart and charts/ip/chart are versioned X.Y.Z, and
  python/pyproject.toml X.Y.Z, matching the tag the release workflow
  will stamp from.
```

```bash
git add CHANGELOG.md charts/cp/chart/Chart.yaml charts/ip/chart/Chart.yaml python/pyproject.toml python/uv.lock
git commit -s -F - <<'MSG' ... MSG
git push -u origin release/vX.Y.Z
gh pr create --title "chore(release): prepare vX.Y.Z" --body-file - <<'BODY' ... BODY
```

Report the PR URL and that it needs another maintainer's approval and merge. Then go to
Step 6.

### Step 6. Wait for the merge

Offer to wait. If the user agrees, poll rather than ask them to come back:

```bash
gh pr view <number> --json state,mergeCommit --jq '"\(.state) \(.mergeCommit.oid // "")"'
```

Check every few minutes until the state is `MERGED`, then continue to Phase 2 with the
merge commit. If the state becomes `CLOSED` without a merge, stop and report. If the user
would rather not wait, tell them to run this skill again after the merge; Step 0 picks up
from the merged PR.

Never merge the release PR yourself, and never approve it.

## Phase 2. Publish

### Step 7. Create the GitHub Release on the merge commit

```bash
git checkout main && git pull --ff-only
MERGE_SHA=$(gh pr view <number> --json mergeCommit --jq .mergeCommit.oid)
```

The release always targets `MERGE_SHA`, even if `main` has moved on: that commit carries
the changelog and versions being released. Confirm the tree there agrees with the version
before tagging anything:

```bash
git show "$MERGE_SHA:charts/cp/chart/Chart.yaml" | grep '^version:'
git show "$MERGE_SHA:python/pyproject.toml"      | grep '^version'
```

Wait for `main.yml` on that commit to finish, because the release retags the images it
pushes and fails if they are missing:

```bash
RUN=$(gh run list --workflow=main.yml --commit "$MERGE_SHA" --limit 1 --json databaseId --jq '.[0].databaseId')
gh run watch "$RUN"
gh run view "$RUN" --json jobs --jq '.jobs[].steps[] | select(.conclusion=="failure") | .name'
```

Judge the run by its steps, not its overall conclusion: the post-job cache-save step has
been failing with "no space left on device" after the push steps succeeded. If "Build and
push both images" and "Package and push both charts" passed, the artifacts exist and the
release can proceed. If either of those failed, stop and report.

Write the release notes: the `## [X.Y.Z]` section of `CHANGELOG.md` at `MERGE_SHA`, from
the line after the heading up to the next `## [` heading, into a temporary file. Then
create the release, which creates the tag and publishes in one step:

```bash
gh release create vX.Y.Z --target "$MERGE_SHA" --title "vX.Y.Z" --notes-file <notes> [--prerelease]
RUN=$(gh run list --workflow=release.yml --limit 1 --json databaseId --jq '.[0].databaseId')
gh run watch "$RUN" --exit-status
```

`--prerelease` for release candidates only. Never `git tag` and push separately: a bare
tag triggers nothing, and the single `gh release create` is the whole step. Never use
`--draft`, which does not fire the workflow.

### Step 8. Verify

```bash
docker buildx imagetools inspect ghcr.io/nvidia/ccluster-cp-operator:X.Y.Z
docker buildx imagetools inspect ghcr.io/nvidia/ccluster-ip-operator:X.Y.Z
helm show chart oci://ghcr.io/nvidia/charts/ccluster-cp --version X.Y.Z
helm show chart oci://ghcr.io/nvidia/charts/ccluster-ip --version X.Y.Z
pip index versions ccluster-py
gh release view vX.Y.Z   # two chart .tgz plus the wheel and sdist attached
```

Report each artifact as published or not. For a failed job give its name and first error
line. If the `python` job failed on trusted publishing, the images and charts are still
published: do not delete the release; fix the PyPI publisher and
`gh run rerun <run-id> --failed`.

## First release only

Before Phase 1, confirm with the user that the one-time setup exists, because the
workflow cannot create it: a PyPI *pending publisher* for `ccluster-py` pointing at this
repository, workflow `release.yml`, environment `pypi`; and the four GHCR packages
(`ccluster-cp-operator`, `ccluster-ip-operator`, `charts/ccluster-cp`,
`charts/ccluster-ip`) set to public if anonymous pulls are expected.
