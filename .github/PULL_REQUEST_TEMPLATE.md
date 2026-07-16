<!--
⚠️ PR TITLE FORMAT (required check: "Lint PR title")

Your PR title must follow Conventional Commits, because it becomes the squashed
commit message on `main`:

    <type>(<optional scope>): <summary>

Allowed types: feat | fix | docs | style | refactor | perf | test | build | ci | chore | revert
Examples:
    feat(catalog): add retry policy to job_deployment
    fix: correct memory normalization for Gi values
    docs(readme): document Argo CD rendering

A PR with a non-conforming title will be blocked from merging.
-->

## Description
<!-- Provide a standalone description of changes in this PR. -->
<!-- Reference any issues closed by this PR with "closes #1234". -->
<!-- Note: this description becomes the body of the squashed commit on `main`. -->

## Checklist
- [ ] I am familiar with the [Contributing Guidelines](https://github.com/NVIDIA/ccluster/blob/main/CONTRIBUTING.md).
- [ ] My PR title follows [Conventional Commits](https://www.conventionalcommits.org/) — `type(scope): summary` (see the note at the top).
- [ ] My commits are signed off (DCO): `git commit -s` — see [CONTRIBUTING.md](https://github.com/NVIDIA/ccluster/blob/main/CONTRIBUTING.md).
- [ ] New or existing tests cover these changes.
- [ ] The documentation is up to date with these changes.
