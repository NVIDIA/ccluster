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
| Charts | `make helm-generate`, `make helm-lint`, `make helm-lint-catalogs` | Generated chart consistency and rendering, and that every catalog chart lints and renders. CI also checks the vendored Argo CD CRDs and server-side diff setting. |
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
outcome) is proven without the real charts.

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

Docker, kind and uv must be available. E2e creates two clusters, a control plane
`KIND_CLUSTER_E2E_CP` (default `ccluster-test-e2e-cp`) and a separate infra plane
`KIND_CLUSTER_E2E_IP` (default `ccluster-test-e2e-ip`), the same shape as the development
pair `make init` creates, and deletes both when done. Never use a working cluster for
either name: the suite uninstalls operators and CRDs. Use `make cleanup-test-e2e` if an
interrupted run leaves the test clusters behind.

### Two clusters and the infra-plane targets

The control-plane cluster is the current kubeconfig context, so a plain `kubectl` in a spec
reaches it; `utils.IP().Kubectl(...)` (or `utils.CP()`) names a cluster explicitly. The cp
operator runs on the control plane, and the infra-plane operator on both clusters, deployed
and removed by the suite (`e2e_suite_test.go`): each is an infra plane.

Specs about a real infra plane run once per target in `targets()` (`test/e2e/clusters_test.go`),
each registered the way a user would register that kind of cluster:

| Target | Cluster | Registration |
| --- | --- | --- |
| `in-cluster` | control plane | A credential-free Secret for `https://kubernetes.default.svc`, as `examples/quickstart` does. Argo CD and the cp operator each use their own ServiceAccount, which covers the cp role's `cclclusterconfigs` rule. |
| `ex-cluster` | infra plane | `ccluster register` from `python/`, run with uv: a cluster-admin token and the cluster's CA, with the server on kind's Docker network (`https://<cluster>-control-plane:6443`). |

To run a spec against both, generate it in a `for _, t := range targets()` loop, register with
`t.register(planeName)`, and reach the plane's cluster with `t.cluster.Kubectl`. Give each
target its own plane name. Delete the plane inline with `deleteInfraPlane`, never from
`DeferCleanup`: its finalizer needs the operator running. Each cluster has one
`CCLClusterConfig`, so a spec holding two planes on one cluster at once sees the second
report `ClusterConfigApplied=Conflict`.

Specs that only test the control plane's handling of a registration - Secret mirroring,
`ApplicationCRDMissing` - use a placeholder server such as `https://infra.example.com:6443`
that is never reachable. The cp operator still dials it, for the remote watch and the
`CCLClusterConfig` write, and reports `ClusterConfigApplied=ClusterUnreachable`; these specs
do not assert `ClusterConfigApplied`. `ClusterConfigApplied=CRDMissing` is covered on the
ex-cluster target, which is registered before the infra-plane operator is installed there
and gets it only once the plane reports `CRDMissing`.

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
Argo CD skips duplicate-server cluster Secrets, and two planes registering the same
cluster at once collide. These contracts are explained in
[DESIGN.md](../DESIGN.md).
