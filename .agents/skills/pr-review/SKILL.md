---
name: pr-review
description: Review a ccluster pull request. Use when the user asks to review, look over, or check a PR, branch or diff. Reads the change against the repository's conventions in AGENTS.md and DESIGN.md, verifies generated files and tests, and reports ranked findings. Does not post to GitHub unless asked.
---

# Pull request review

Review the change as a maintainer would: is it correct, does it respect the invariants
this repository documents, and will it still be right after the generators and CI run.
Report findings; do not fix them unless the user asks.

## 1. Load the change

```bash
gh pr view <number> --json title,body,baseRefName,headRefName,files,additions,deletions
gh pr diff <number>
gh pr checks <number>
```

For a local branch use `git diff main...HEAD` and `git log main..HEAD`. Read every
changed file in full where the diff alone does not show the surrounding logic.

## 2. What to check, in this order

**Correctness first.** Trace the changed code paths with concrete inputs. Look for
nil dereferences, wrong error handling, off-by-one in ordering logic, requeue loops,
non-idempotent reconciles, and status updates that skip `observedGeneration`.

**Repository invariants** (from `AGENTS.md`; a change that breaks one is a bug even if
tests pass):

- Controller ordering and ownership: `Available=True` on a CCLInfraPlane implies its
  AppProject exists; release is monotonic; ownership is by annotation, never
  `ownerReference`; no ApplicationSet or RollingSync; AppProject `sourceRepos` is the
  catalog only, never `*`; cluster Secrets stay in the Argo CD namespace.
- RBAC markers live in the package of the operator that needs them; a marker in the
  wrong package grants the wrong operator.
- The operator never installs Argo CD.
- Catalog packages: no mirror of the API types, pointer optionals with `omitempty`,
  non-pointer required fields without `omitempty`, snake_case directories, a new
  component added to `hack/pyschema/main.go` as well as `cluster_base.go`.
- CRDs: minimal spec surface, nothing that duplicates metadata or an operator flag,
  `metav1.Condition` for status.
- Leader election IDs and the `-cp` / `-ip` split are untouched.

**Generated files.** If the PR touches `api/`, any `+kubebuilder:` marker or doc comment
on an API type, `config/`, `catalogs/**/*.go` or `hack/pyschema/`, confirm the generated
outputs are in the PR and match. Run the generators locally on the PR head when in doubt:

```bash
make manifests generate && git status --short
make helm-generate && git status --short charts/
make python-generate && git status --short python/
```

Hand edits under `charts/*/chart/templates/`, `config/crd/bases/`,
`config/cp/rbac/role.yaml`, `zz_generated.*.go` or the generated Python packages are
findings: they will be overwritten.

**Tests.** New behaviour needs an envtest spec in `internal/controller/cp/`; a new RBAC
need is only proven by `make test-e2e`. Table tests are for states envtest cannot reach,
not for coverage. Check that assertions test the contract, not the implementation.

**Docs and metadata.** `CHANGELOG.md` has an `Unreleased` entry for user-visible changes,
and the PR only adds entries: rewording, reordering or removing someone else's
`Unreleased` entry, or touching a released version's section, is a finding, because the
changelog is append-only until the `release` skill consolidates it.
`AGENTS.md`, `DESIGN.md` and `README.md` still describe what the code now does. The PR
title is a valid Conventional Commit and the body follows the plain-text format (prose,
blank line, hyphen bullets, no Markdown), since it becomes the commit message.

**CI.** `lint`, `test`, `test-e2e` and `test-python` have `paths:` filters; a skipped
check is expected for PRs outside their inputs, but a PR that changes an input and shows
the check skipped means the filter is stale. That is a finding against the filter.

## 3. Report

Rank findings most severe first. For each: file and line, one sentence stating the
defect, and the concrete scenario in which it goes wrong. Separate hard findings from
suggestions, and say explicitly what was verified by running something versus by
reading. If nothing is wrong, say so plainly and name what was checked.

Do not post comments or a review to GitHub unless the user explicitly asks. When asked,
post with `gh pr review <number> --comment --body-file <file>` (or `--approve` /
`--request-changes` only when the user chooses one) and keep the same plain-text style
as commit bodies.
