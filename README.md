# CCluster

**An efficiency-focused, Kubernetes-native platform for managing the end-to-end AI lifecycle across distributed, multi-cloud GPU infrastructure.**

CCluster aims to give AI teams a single control plane for their GPU fleet — provisioning clusters, deploying and scaling workloads, and running the full model lifecycle (inference, fine-tuning, batch jobs, and training) across clouds and regions, while keeping expensive accelerators as busy and as cost-effective as possible.

> **Project status: early.** CCluster is at the start of an ambitious roadmap. What exists today is the first working slice of the control plane — two `v1alpha1` CRDs that register clusters with Argo CD and roll out ordered bundles of Helm charts onto them (see [DESIGN.md](DESIGN.md)) — plus the chart catalog they deploy from. The broader platform described below is the vision we're building toward, not yet shipped. Expect rapid, breaking change.

# Vision

GPU capacity is scarce, expensive, and increasingly spread across many clouds, regions, and on-prem sites. Teams end up stitching together bespoke tooling for each environment, and utilization suffers. CCluster's goal is to make a heterogeneous, multi-cloud GPU fleet feel like one efficient, Kubernetes-native platform.

Guiding principles:

- **Efficiency first.** Maximize accelerator utilization through bin-packing, autoscaling, and workload-aware scheduling — treating idle GPU time as the primary cost to eliminate.
- **Kubernetes-native.** Everything is expressed as Kubernetes resources and reconciled declaratively (GitOps via Argo CD), so the platform composes with the existing cloud-native ecosystem instead of replacing it.
- **Multi-cloud by default.** A cluster on AWS, GCP, Azure, Nebius, or on-prem is a first-class citizen; workloads and policy span providers without per-cloud rewrites.
- **End-to-end lifecycle.** One platform for the whole journey — from provisioning a cluster, to training and fine-tuning, to batch and interactive compute, to production inference and autoscaling.
- **Typed and validated.** Deployment configuration is modeled as CRD schemas with server-side validation, so misconfiguration is rejected by the API server rather than discovered at sync time.

See [DESIGN.md](DESIGN.md) for what's shipped today — the control-plane API, the ordering
algorithm, and the chart catalog it deploys from — and why it's built the way it is.

# Getting Started

## Deploying the operators

Each operator ships as a Helm chart. The control-plane chart also installs Argo CD, which
the control plane depends on — CCluster reconciles clusters and workloads by creating Argo CD
`Application` resources, so Argo CD is a prerequisite rather than something the operator
installs for itself.

```bash
# Control-plane cluster: ccluster-cp-operator + Argo CD
make deploy-cp

# Infra-plane cluster: ccluster-ip-operator
make deploy-ip
```

Both targets act on your current kubeconfig context. Pin it first:

```bash
export KUBECONFIG=~/.kube/my-cluster.yaml
```

If the cluster already runs Argo CD, install the operator on its own with
`make deploy-cp ARGOCD=false`. The charts live in `charts/cp/chart` and `charts/ip/chart`
and can also be installed directly with `helm install`; they are generated from the
kustomize overlays in `config/`, so regenerate them with `make helm-generate` after changing
the API or any RBAC marker.

## Trying it out

With the operators deployed and a kubeconfig pinned:

```bash
# Register a cluster (the samples register the control-plane cluster with itself)
kubectl apply -f examples/quickstart/v1alpha1_cclinfraplane.yaml

# Roll out an ordered bundle of catalog charts onto it
kubectl apply -f examples/quickstart/v1alpha1_cclcomponentset.yaml

kubectl -n ccluster-quickstart get cclinfraplanes
kubectl -n ccluster-quickstart get cclcomponentsets
# The Applications sit next to the CCLComponentSet, in its namespace, under an Argo CD
# AppProject named after that namespace.
kubectl get applications -L ccluster.nvidia.com/wave
```

The sample `CCLComponentSet` deploys from `--catalog-repo-url` at `--catalog-revision`
(`https://github.com/NVIDIA/ccluster` at `main` by default). Argo CD clones that repository
itself, so if it is private give it a token with read access:
`make deploy-cp GITHUB_TOKEN=...`. Point it at a fork or branch with
`make deploy-cp CATALOG_REPO_URL=... CATALOG_REVISION=...`.

## Local development

CCluster's local loop runs both operators in a pair of [kind](https://kind.sigs.k8s.io/)
clusters, `ccluster-cp` and `ccluster-ip`, so the control-plane and infra-plane operators
stay isolated the same way they are in production. Docker must be running first.

```bash
make init    # create both kind clusters (idempotent)
make build   # manifests, CRDs, deepcopy, both binaries, both images, both charts
make load    # build each operator image and load it into its own cluster
make deploy  # helm install: cp into ccluster-cp, ip into ccluster-ip
```

Each of those has `-cp` / `-ip` halves (`make build-cp`, `make deploy-ip`, ...) if you only
want to touch one operator. `make clean` deletes both clusters; `make clean-all` also wipes
`bin/` and `dist/`, so the next build re-downloads `controller-gen`, `kustomize`, `envtest`
and `kubebuilder`.

For the fastest inner loop, run the control-plane manager on your host against the cluster's
API server instead of building an image each time — this installs the CRDs but not the chart,
so don't combine it with `make deploy-cp`:

```bash
export KUBECONFIG=~/.kube/ccluster-dev.yaml   # see the warning below
make install    # CRDs only
make run-cp     # runs the manager locally; Ctrl-C to stop
```

Note this loop runs as your kubeconfig identity, not the operator's ServiceAccount, so it
does not exercise RBAC — use `make load-cp` and a `rollout restart` for that.

**Every cluster-touching target acts on the current kubeconfig context, with no
confirmation.** Development machines often carry other contexts (EKS, GKE, teleport) in the
default kubeconfig, and `kind create cluster` switches the current context out from under
you. Pin a dedicated kubeconfig before running any of the above:

```bash
kind get kubeconfig --name ccluster-cp > ~/.kube/ccluster-dev.yaml
export KUBECONFIG=~/.kube/ccluster-dev.yaml
```

See [AGENTS.md](AGENTS.md) for the full set of build, test and Helm-generation targets,
including RBAC, CRD and chart regeneration after an API change.

# Requirements

- **Go:** >= 1.25, to build the operators.
- **Deployment targets (external):** a Kubernetes cluster and [Helm](https://helm.sh/).
  [Argo CD](https://argo-cd.readthedocs.io/) reconciles the charts and is installed into the
  control-plane cluster by `make deploy-cp`. GPU workloads additionally require the NVIDIA
  GPU Operator / drivers on the cluster.
- **Local development:** [kind](https://kind.sigs.k8s.io/) and Docker. `make init` creates
  both development clusters; `make build load deploy` builds, loads and installs into them.

## Releases

- **Changelog:** [`CHANGELOG.md`](CHANGELOG.md)
- Broader direction and upcoming capabilities are described in [DESIGN.md](DESIGN.md).

# Contribution Guidelines

CCluster is early and evolving quickly — contributions, ideas, and feedback on the direction are welcome.

- Start here: [`CONTRIBUTING.md`](CONTRIBUTING.md)
- Working on the operators: [`AGENTS.md`](AGENTS.md) for the build and generation mechanics,
  [`DESIGN.md`](DESIGN.md) for how the control plane works.
- Development quickstart:

```bash
make build          # manifests, CRDs, deepcopy, both binaries, both images, both charts
make test           # envtest suites for both control-plane controllers
make helm-generate  # regenerate the charts from config/; CI fails on any drift
```

## Maintainers

- **Maintainers:** [`MAINTAINERS.md`](MAINTAINERS.md)
- **Code of Conduct:** [`CODE_OF_CONDUCT.md`](CODE_OF_CONDUCT.md)

## Security

- Vulnerability disclosure: see [`SECURITY.md`](SECURITY.md).
- Please do **not** file public GitHub issues for security reports.

## Support

- **Level:** Maintained (best-effort; early and evolving quickly).
- **How to get help:** see [`SUPPORT.md`](SUPPORT.md) — GitHub Issues and Discussions.

# Community

Questions, ideas, and feedback on the direction are welcome via
[GitHub Issues](../../issues) and [Discussions](../../discussions). See
[`SUPPORT.md`](SUPPORT.md) for the best channel for your question.

# License

This project is licensed under the Apache License 2.0 — see the [LICENSE](LICENSE) file for details.
