<!--
⚠️  PR TITLE must follow Conventional Commits — it becomes the squashed commit
    subject on `main` and is enforced by the "Lint PR title" check:

        <type>(<optional scope>): <summary>

    types: feat | fix | docs | style | refactor | perf | test | build | ci | chore | revert
    e.g.   feat(catalogs): add retry policy to job_deployment

BEFORE OPENING THIS PR:
  • Sign off every commit (DCO):  git commit -s   (enforced by "Check Signed-off-by")
  • Add or update tests where it makes sense
  • Update docs (README / docs/ / python/README.md) if behavior changed

HOW THIS BODY IS USED:
    Everything inside HTML comments (like this block) is stripped from the commit
    message. Whatever visible text you write below becomes the BODY of the
    squashed commit on `main` — so write it for someone reading `git log`.
    Reference issues you close, e.g. "closes #123".
-->
