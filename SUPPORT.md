# Support

## Support level: **Maintained**

CCluster is **actively maintained** on a best-effort basis by its
[maintainers](MAINTAINERS.md). The project is early and evolving quickly —
expect rapid, sometimes breaking, change. There is no formal SLA, but
maintainers monitor issues and pull requests and aim to respond in a timely
manner.

## How to get help

| I want to... | Go to |
| --- | --- |
| Report a bug | [Open a bug issue](../../issues/new/choose) |
| Request a feature | [Open a feature issue](../../issues/new/choose) |
| Ask a question / discuss an idea | [GitHub Discussions](../../discussions) or an issue |
| Contribute a change | [CONTRIBUTING.md](CONTRIBUTING.md) |
| Report a security vulnerability | [SECURITY.md](SECURITY.md) — **do not** open a public issue |

## Response expectations

- **Best-effort.** Maintainers triage issues and PRs as capacity allows.
- Please search existing issues before opening a new one.
- Well-scoped reports with reproduction steps and version information get
  resolved fastest.

## What is in scope

Support covers what this repository publishes: the operators and their Helm charts, the
`ccluster` command line, the Python and Go SDKs, and the catalog charts under `catalogs/`. The upstream projects wrapped by `cluster_base` (Istio,
cert-manager, GPU Operator, etc.) are maintained by their respective
communities — see [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).
