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
- `ccluster-py` installs a `ccluster` command built on click. ...

### Removed
- **Breaking:** `compute_deployment` drops `sshUsername`. ...
```

Use the subsections `Added`, `Changed`, `Deprecated`, `Removed`, `Fixed` and `Security`,
in that order, and mark incompatible changes `**Breaking:**`. Edit only your own
fragment; a follow-up PR in the same cycle may amend it, and one after a release adds a
new fragment. Internal refactors, CI and docs-only changes usually need none.
