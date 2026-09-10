---
name: commit-pr
description: Commit work in ccluster and open a pull request. Use whenever the user asks to commit, write a commit message, push, or open/raise a PR. Covers branch naming, DCO sign-off, the plain-text commit body format, the Conventional Commit PR title, pre-commit checks and the CHANGELOG entry.
---

# Commit and open a pull request

The repository squash-merges with the PR title as the commit subject and the PR body as
the commit body. Every commit message and PR description is therefore read mostly in
`git log` in a terminal, and that drives the format below.

## 1. Branch

Never commit on `main`. If on `main`, create a branch named `<type>/<short-topic>` using
the same Conventional Commit type the PR will carry, e.g. `feat/appproject-per-namespace`,
`ci/lint-path-filters`, `docs/release-process`.

```bash
git checkout -b <type>/<short-topic>
```

## 2. Pre-commit checks

Run what applies to the files being committed. Skip a group only if none of its inputs
changed.

| Changed | Run |
| --- | --- |
| any `.go`, `go.mod`, `go.sum` | `make fmt vet lint-fix` then `make test` |
| `api/`, any `+kubebuilder:` marker or doc comment on an API type | `make manifests generate` |
| `config/`, `api/`, RBAC markers | `make helm-generate && git status --short charts/` (must be clean) and `make helm-lint` |
| `api/`, `config/crd/`, `catalogs/**/*.go`, `hack/pyschema/`, `python/` | `make python-generate && git status --short python/` (must be clean) and `make python-test` |
| `test/e2e/` | `make test-e2e` when feasible; say so in the PR if not run |

`make python-build` dirties `python/pyproject.toml` with the local version. Do not commit
that change: run `git checkout python/pyproject.toml` first.

## 3. CHANGELOG

`CHANGELOG.md` follows Keep a Changelog. If the change is visible to a user of the
operators, charts, catalog or Python package, add a bullet under `## [Unreleased]` in the
matching subsection (`Added`, `Changed`, `Deprecated`, `Removed`, `Fixed`, `Security`).
Mark incompatible changes `**Breaking:**`. Internal refactors, CI and docs-only changes
usually need no entry. Stage the CHANGELOG with the same commit.

The changelog is **append-only between releases**. Add your entry; do not reword,
reorder, merge or delete entries other people wrote, and never touch a released
version's section. The only exception is your own entry from the same unreleased cycle,
which you may amend (for instance when a follow-up PR changes what you shipped). The
`release` skill is the one place the `Unreleased` list gets consolidated.

## 4. Stage and commit

Stage explicit paths, never `git add -A`. Every commit is signed off (DCO is enforced):

```bash
git add <paths>
git commit -s -F - <<'MSG'
<type>(<scope>): <summary>

<body>
MSG
```

If your agent tooling adds a co-author trailer of its own, it goes after the body,
separated by a blank line, and is left out of the PR description.

### Subject line

Conventional Commit, enforced on the PR title by `.github/workflows/pr-title.yml`:

- Types: `feat`, `fix`, `docs`, `style`, `refactor`, `perf`, `test`, `build`, `ci`,
  `chore`, `revert`. Append `!` before the colon for a breaking change.
- Scope is optional, lowercase, and names the area: `cp`, `ip`, `api`, `catalogs`,
  `charts`, `python`, `release`, `e2e`, `images`, `readme`. Omit it for cross-cutting
  changes (`ci: ...`, `docs: ...`).
- Summary: imperative mood, lowercase after the colon, at least 10 characters, no trailing
  period, under about 70 characters. Say what the change does, not what you did
  ("publish charts on main", not "added chart publishing").

### Body

Plain text, not Markdown. It is the squash-merge commit message.

1. One or two sentences of prose saying what the change does and why, in the imperative.
2. A blank line, then a hyphen bullet list of the major changes. Each bullet is one or two
   complete sentences that name the concrete thing that changed and say what it does now.
   Order by importance, not by file.

Keep it short. The body is a summary a reviewer reads before the diff, not a narration of
the diff: three to five bullets for a typical PR, one line of prose where one is enough,
and nothing a reader would recover by opening the changed file. Leave out how you found
the problem, what you tried first, file-by-file inventories, and anything a bullet above
already implies. A rule of thumb is a body of at most about fifteen lines; a change that
seems to need more is usually two PRs.

Rules:

- No backticks, bold, headers, or any other Markdown syntax. Write identifiers bare:
  CCLInfraPlaneReconciler, make deploy-cp, data.project.
- Wrap at about 72 columns.
- No "Test plan", no checkbox lists, no "Generated with" footer. If verification is itself
  part of the change (a new e2e spec, a fixed assertion), it is a bullet.
- No migration notes or backward-compatibility caveats; the project is pre-release.

A body in the intended shape:

```
Make the Kubernetes namespace the tenancy boundary for everything the
control-plane operator writes into Argo CD.

- CCLInfraPlaneReconciler creates one AppProject per namespace, before
  mirroring the cluster Secret or reporting Available. The project is
  deleted by the last non-terminating CCLInfraPlane in the namespace.
- Mirrored cluster Secrets are scoped to that project via data.project
  and renamed <namespace>-<name>.
- Applications now live in the CCLComponentSet's own namespace rather
  than the Argo CD namespace.
```

Prefer one commit per PR. If there are several, the PR body must still stand alone,
because only the squashed message survives; see step 7.

## 5. Push and open the PR

The PR title is the commit subject, the PR body is the commit body, verbatim:

```bash
git push -u origin <branch>
gh pr create --title "<type>(<scope>): <summary>" --body-file - <<'BODY'
<body exactly as in the commit, without any trailers>
BODY
```

Do not add labels, reviewers or a draft flag unless asked. Report the PR URL to the user.

## 6. After opening

Check the status once and report:

```bash
gh pr checks <number>
```

`lint`, `test`, `test-e2e` and `test-python` carry `paths:` filters and legitimately show
as skipped for PRs that do not touch their inputs. `test-chart`, `dco` and `pr-title`
always run. Fix a red check on the same branch with another signed-off commit; do not
force-push over review history unless the user asks for a rebase.

## 7. Adding commits to an open PR

Every push to an open PR is followed by a fresh look at the PR description, because the
description, not the commit list, becomes the squash-merge commit body.

1. Check the PR is still open before pushing anything to its branch:
   `gh pr view <number> --json state --jq .state` must print `OPEN`. A PR someone merged
   or closed in the meantime gets a new branch from `main` and a new PR, never a push to
   the old branch and never an edit of the merged PR's title or body.
2. Commit the new work with the same rules as step 4 (its own subject and body are for
   the branch history only).
3. Re-read the whole diff against the base, not just the new commit:
   `gh pr diff <number>`.
4. Rewrite the PR body so it describes the PR as it now is. Fold in what the new commit
   changed, drop bullets that no longer hold (a first attempt replaced by a second, a
   fix to something introduced earlier in the same PR), and re-order by importance. Do
   not append a "follow-up" or "also" bullet describing the fixup; the reader of the
   commit on main never saw the intermediate state. Adjust the title if the scope moved.
5. Apply it:

```bash
gh api -X PATCH repos/{owner}/{repo}/pulls/<number> -f title="<type>(<scope>): <summary>" -F body=@<file>
```

(`gh pr edit` also works in principle, but on this repository it fails with a GraphQL
error about deprecated classic Projects and leaves the body unchanged; the REST call
above does not.)

Review fixes count too: a review round that only tightened wording may leave the body
unchanged, but check it rather than assume. Keep the length discipline from step 4; the
body should not grow with every push.

