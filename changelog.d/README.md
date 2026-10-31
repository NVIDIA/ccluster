# Unreleased changes

Each pull request with a user-visible change adds one file here instead of editing
[`CHANGELOG.md`](../CHANGELOG.md). A release collects every file into a new version
section of the changelog and deletes them, so an entry always lands in the first release
cut after its pull request merges, whenever its branch was started.

Name the file after the branch topic, `<short-topic>.md` (branch `feat/ccluster-cli` adds
`ccluster-cli.md`). It holds [Keep a Changelog](https://keepachangelog.com/en/1.1.0/)
subsections and bullets only, with no version heading:

```markdown
### Added
- A `ccluster` command line, released as a binary for each platform. ...

### Removed
- **Breaking:** `compute_deployment` drops `sshUsername`. ...
```

Use the subsections `Added`, `Changed`, `Deprecated`, `Removed`, `Fixed` and `Security`,
in that order, and mark incompatible changes `**Breaking:**`. Internal refactors, CI and
docs-only changes usually need none.

A fragment still in this directory describes unreleased behaviour, so a later pull request
that changes or reverts that behaviour amends the fragment, whoever wrote it, rather than
adding a bullet that undoes it: the release notes describe what ships, not how it got
there. Put the pull request's own new changes in its own fragment. Once a release has
collected a fragment, describe further changes in a new one.
