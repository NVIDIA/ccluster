---
name: release
description: Cut a ccluster release end to end, except approving the release PR. Use when the user asks to release, cut a version, bump versions, prepare or finish a release, or tag a release. Detects where the release stands (no branch, release PR open, PR merged, tag created) and continues from there, so it can be run once before the PR is approved and once after.
---

# Release

A release is one reviewed pull request followed by one GitHub Release, and this skill does
all of it except the approval:

1. **Phase 1, prepare.** Cut `release/vX.Y.Z` from `main`, agree the version with the
   user, consolidate the `changelog.d/` fragments into `CHANGELOG.md` under `X.Y.Z`,
   write the version to the `VERSION` file, commit, and open the release PR with the
   `release` label.
2. **Approval.** Another maintainer reviews and merges the PR. The skill never merges it.
   The label makes CI run the full e2e tier, so the release commit is the one commit
   proven against real Argo CD.
3. **Phase 2, publish.** Create the GitHub Release `vX.Y.Z` on the merge commit with
   `gh`. That creates the tag and fires `.github/workflows/release.yml`, which checks the
   tag against `VERSION`, retags the `:<sha>` images, re-packages the charts, builds the
   `ccluster` CLI tarballs, publishes `ccluster-py==X.Y.Z` to PyPI and attaches the
   assets. Verify the artifacts.

Nobody has to touch the GitHub UI. Nothing is ever rebuilt for a release: the images a
release ships are the ones `main.yml` pushed for the merge commit, and the workflow
stamps the version from the tag (`VERSION=${tag#v}`). The root `VERSION` file is the one
committed version; `make check-version` fails the release if the tag disagrees with it,
which is what ties a tag to the commit that carries its changelog.

## Step 0. Find out where the release stands

Always start here, on a clean checkout with `gh auth status` working:

```bash
git checkout main && git pull --ff-only && git status --short
LAST=$(gh release list --exclude-drafts --limit 20 --json tagName,isPrerelease \
         --jq '[.[] | select(.isPrerelease == false)][0].tagName')   # empty before the first release
gh pr list --state all --search 'in:title "release: new ccluster version"' --limit 5 \
  --json number,title,state,headRefName,mergeCommit
```

Then branch on what you find:

| State | Go to |
| --- | --- |
| No open or recently merged release PR | Phase 1 |
| Release PR open, not merged | Tell the user it awaits approval, give the URL, and offer to wait (Step 6). Do not merge. |
| Release PR merged, no `vX.Y.Z` tag yet | Phase 2 |
| Tag and release exist | Step 8, verify, then report |

The version in the PR title (`release: new ccluster version vX.Y.Z`) is the version for
the later phases.

## Phase 1. Prepare the release PR

### Step 1. Agree the version

Suggest a version, then ask the user to confirm or override it. This is the one question
the skill asks.

- Latest release: `LAST` from Step 0 (`v0.0.0` if none; the first release is then `v0.1.0`
  unless the user says otherwise).
- Read every fragment in `changelog.d/` (all `*.md` except `README.md`). Following
  Semantic Versioning on the `0.y.z` line: any `**Breaking:**` entry or `Removed`
  section, or anything under `Added` or `Changed`, suggests a minor bump; only `Fixed`,
  `Security` or `Deprecated` suggests a patch bump.
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

`CHANGELOG.md` follows Keep a Changelog. Between releases, PRs never edit it: each adds a
fragment `changelog.d/<short-topic>.md` (format in `changelog.d/README.md`). The release is
the one point where fragments become changelog entries.

- Read every fragment: `ls changelog.d/*.md | grep -v README.md`.
- Add an entry for any user-visible merged PR since the last release that has no
  fragment, from its title and body. Do not invent detail that is not in them.
- Consolidate into one new section: merge entries that describe the same feature from
  different angles, drop entries superseded within the same cycle (a thing added and then
  renamed ships once, under its final name), and keep one bullet per user-facing change.
  Keep the `**Breaking:**` markers. Preserve the subsection order Added, Changed,
  Deprecated, Removed, Fixed, Security and leave out empty subsections.
- Insert the section as `## [X.Y.Z] - YYYY-MM-DD` (today, UTC) above the latest released
  version, below the preamble.
- Delete the fragments it consumed, keeping `README.md`:
  `git rm $(ls changelog.d/*.md | grep -v README.md)`. A PR that merges while the release
  PR is open leaves its fragment on `main` for the next release. If you update the release
  branch from `main`, consolidate and delete any fragment that arrives with it. If that hits
  a modify/delete conflict on a fragment, carry its amended text into the new section
  before deleting it.
- Add the comparison link for the new version at the top of the link list at the bottom
  of the file:

```
[X.Y.Z]: https://github.com/NVIDIA/ccluster/compare/vPREV...vX.Y.Z
```

For the first release the link is
`https://github.com/NVIDIA/ccluster/releases/tag/vX.Y.Z`.

For a release candidate the heading is `## [X.Y.Z-rc.N] - date`. When the final release
follows, fold every rc section since the last final release into the single `X.Y.Z`
section and drop the rc headings and links; the rc tags remain on GitHub.

Do not edit sections of already released versions.

### Step 4. Set the version

The root `VERSION` file is the only committed version. Write the bare version to it:

```bash
echo X.Y.Z > VERSION            # or X.Y.Z-rc.N
sed -i.bak 's|^sshInjectorImage: ghcr.io/nvidia/ccluster-compute-ssh:.*|sshInjectorImage: ghcr.io/nvidia/ccluster-compute-ssh:X.Y.Z|' \
  catalogs/deployments/compute_deployment/values.yaml && rm catalogs/deployments/compute_deployment/values.yaml.bak
make check-version VERSION=X.Y.Z
go test ./catalogs/deployments/compute_deployment/
git status --short              # only CHANGELOG.md, VERSION, that values.yaml and deleted changelog.d/ fragments
```

The `compute_deployment` default launcher image is the one catalog value that names a
release: catalogs are read from git, so the release workflow cannot stamp it. Its test
fails when the tag and `VERSION` differ.

Nothing else is bumped. `charts/*/chart/Chart.yaml` stays at `0.0.0-dev` and
`python/pyproject.toml` at `0.0.0.dev0`; the release workflow stamps every artifact from
the tag, and `check-version` guarantees the tag equals this file.

### Step 5. Commit and open the release PR

The release PR has a fixed title, no description, and the `release` label. The title is
the squash-merge subject; the changelog is the description, so the PR body is empty.

```bash
git add CHANGELOG.md VERSION changelog.d
git commit -s -m "release: new ccluster version vX.Y.Z"
git push -u origin release/vX.Y.Z
gh pr create --title "release: new ccluster version vX.Y.Z" --body "" --label release
```

The `release` label makes `test-e2e.yml` run the full tier (real Argo CD, real rollout)
on this PR instead of the quick one; it takes several minutes longer than usual. Report
the PR URL, that CI is running the full e2e tier, and that it needs another maintainer's
approval and merge. Then go to Step 6.

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
the changelog and the `VERSION` file being released. Confirm they agree before tagging:

```bash
git show "$MERGE_SHA:VERSION"                       # must print X.Y.Z
git show "$MERGE_SHA:CHANGELOG.md" | grep -n "^## \[X.Y.Z\]"
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
push the images" and "Package and push both charts" passed, the artifacts exist and the
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
docker buildx imagetools inspect ghcr.io/nvidia/ccluster-compute-ssh:X.Y.Z
helm show chart oci://ghcr.io/nvidia/charts/ccluster-cp --version X.Y.Z
helm show chart oci://ghcr.io/nvidia/charts/ccluster-ip --version X.Y.Z
pip index versions ccluster-py
gh release view vX.Y.Z   # two chart .tgz, four CLI .tar.gz, the wheel and sdist attached
```

The four CLI tarballs are `ccluster_X.Y.Z_{linux,darwin}_{amd64,arm64}.tar.gz`. To check
one, download it and confirm the binary reports the release:

```bash
gh release download vX.Y.Z --pattern 'ccluster_X.Y.Z_linux_amd64.tar.gz' --dir <tmp>
tar -xzf <tmp>/ccluster_X.Y.Z_linux_amd64.tar.gz -C <tmp>
<tmp>/ccluster_X.Y.Z_linux_amd64/ccluster --version   # ccluster version vX.Y.Z
```

Report each artifact as published or not. For a failed job give its name and first error
line. If the `python` job failed on trusted publishing, the images and charts are still
published: do not delete the release; fix the PyPI publisher and
`gh run rerun <run-id> --failed`.

## First release only

Before Phase 1, confirm with the user that the one-time setup exists, because the
workflow cannot create it: a PyPI *pending publisher* for `ccluster-py` pointing at this
repository, workflow `release.yml`, environment `pypi`; and the GHCR packages
(`ccluster-cp-operator`, `ccluster-ip-operator`, `ccluster-compute-ssh`,
`charts/ccluster-cp`, `charts/ccluster-ip`) set to public if anonymous pulls are
expected. `ccluster-compute-ssh` must be public for `compute_deployment`'s default
launcher image to pull on any cluster.
