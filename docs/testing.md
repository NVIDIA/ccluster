# Testing

Use the smallest layer that proves the changed contract. Controller behaviour belongs in
Ginkgo/Gomega envtest specs; real RBAC, running watches and Argo CD need e2e. The
[commit-pr skill](../.agents/skills/commit-pr/SKILL.md) gives the required pre-commit checks
by changed path. See [Development](development.md) for generation and cluster setup.

## Choosing a test layer

| Layer | Command | What it proves |
| --- | --- | --- |
| Selective unit | `go test -run TestReleaseCount ./internal/controller/cp/` | Pure rollout logic without envtest binaries. Select the relevant named test. |
| Envtest | `make test` | Controller reconciliation against a real API server and etcd; no cluster needed. |
| Quick e2e | `make test-e2e` | Helm deployment, operator ServiceAccount RBAC and running watch wiring for both operators, without Argo CD reconciliation. |
| Full e2e | `make test-e2e-full` | Quick tier plus real Argo CD rollout, ordered release/teardown, drift repair, values updates, tenancy and istio's ignored webhook drift. |
| Charts | `make helm-generate`, `make helm-lint` | Generated chart consistency and rendering. CI also checks the vendored Argo CD CRDs and server-side diff setting. |
| Python | `make python-generate`, `make python-test` | Generated model consistency, example round-tripping and catalog value serialization. |
| Manual host manager | `make run-cp` | Fast iteration under your kubeconfig identity; does not prove operator RBAC. |

Inspect generated diffs: expected output changes belong in the change, and regeneration
should produce no further drift once they are incorporated. No runtime test substitutes
for checking generated chart or Python output.

## Envtest and unit tests

Setup lives in `internal/controller/cp/suite_test.go` and, for the infra-plane operator,
`internal/controller/ip/suite_test.go`. `make test` supplies the envtest assets and
performs manifest generation, formatting and vetting before running tests.

`test/client/` runs the generated Go client in `pkg/client/` against envtest with plain
`testing`: each kind's scope and status subresource, the informer factory and listers,
`Extract` on the server's managed fields, and writes through the fake clientset.

The ip suite drives `CCLClusterConfigReconciler` with an injected `Registry` of test
Verifiers, so the controller's contract (run every Verifier against the spec, report the
outcome) is proven without the seventeen real charts.

The envtest specs call `Reconcile` directly; they do not start controllers in the test
manager. Watch map functions are exercised as ordinary functions, which cannot prove that
a running manager registered the right watches or responds to events. Envtest also runs no
Argo CD: schema-less CRDs under `test/testdata/crd/` stand in for Applications and
AppProjects, and specs set Application status to advance the rollout.

Plain table tests alongside these suites are for states that reconciliation cannot cheaply
stage, such as Applications existing for components 1 and 3 but not 2. Keep them selective;
do not add tests for one-line helpers already covered by behaviour tests. Use `go test -run`
to select these tests without invoking the Ginkgo envtest suite.

## E2e setup and lifecycle

Docker and kind must be available. E2e creates `KIND_CLUSTER_E2E` (default
`ccluster-test-e2e`) and deletes it when done. Never use a working cluster for this name.
Use `make cleanup-test-e2e` if an interrupted run leaves the test cluster behind.

The full tier pulls real images and waits for Argo CD convergence. Argo CD clones the
catalog over the network; a private repository needs `GITHUB_TOKEN`, as with deployment.
The full tier can also be selected with `make test-e2e E2E_FULL=true`. CI runs it for PRs
labelled `release`; other applicable PRs run the quick tier.

### Maintaining the two tiers

The outer Ginkgo `Ordered` container initially deploys with `ARGOCD=false`. This preserves
the `ApplicationCRDMissing` path and verifies starting the operator before Argo CD exists.

The quick tier renders and applies the Argo CD CRDs from the vendored dependency, then
restarts the manager so it registers argoproj watches. With no Argo CD controller present,
specs can prove Application/AppProject writes, drift repair and pruning under the real
operator's RBAC. These CRDs are removed before the full tier installs the dependency, so
Helm does not encounter ownership conflicts. Preserve this lifecycle when changing tests.

The nested `Context("with real Argo CD", Label("full"), Ordered)` upgrades the same Helm
release to add Argo CD and restarts the manager. The quick command excludes `full`; the
full command runs everything. There is no separate quick-only label.

Real rollout assertions must check the managed resources as well as aggregate Application
health where appropriate: large CRDs expose server-side apply/diff regressions that can
stall later components. Real tenancy assertions must use distinct cluster server URLs;
Argo CD skips duplicate-server cluster Secrets. These contracts are explained in
[DESIGN.md](../DESIGN.md).
