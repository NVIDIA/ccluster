# CCluster

[![License](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](LICENSE)
[![Main](https://github.com/NVIDIA/ccluster/actions/workflows/main.yml/badge.svg?branch=main)](https://github.com/NVIDIA/ccluster/actions/workflows/main.yml)

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
`make deploy-cp ARGOCD=false ARGOCD_NAMESPACE=<its namespace>`, such as `argocd`.

The `make deploy-*` targets install the charts from this checkout with a locally built
image. Released versions are published to GHCR and can be installed without a clone:

```bash
# Control-plane cluster: ccluster-cp-operator + Argo CD
helm install ccluster-cp oci://ghcr.io/nvidia/charts/ccluster-cp --version X.Y.Z \
  -n ccluster-cp-system --create-namespace

# Infra-plane cluster: ccluster-ip-operator
helm install ccluster-ip oci://ghcr.io/nvidia/charts/ccluster-ip --version X.Y.Z \
  -n ccluster-ip-system --create-namespace
```

Each released chart references `ghcr.io/nvidia/ccluster-{cp,ip}-operator:X.Y.Z` and pins
`--catalog-revision` to the matching `vX.Y.Z` tag. The Pydantic models for the API and the
catalog ship as `pip install ccluster-py` (imported as `ccluster`).

The charts themselves live in `charts/cp/chart` and `charts/ip/chart`; they are generated
from the kustomize overlays in `config/`, so regenerate them with `make helm-generate`
after changing the API or any RBAC marker.

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

In the infra-plane cluster, its operator reports whether every `catalogs/cluster_base`
component verifies against the cluster's `CCLClusterConfig` (every component's check is a
placeholder reporting true for now). The control-plane operator writes that object into
every cluster a `CCLInfraPlane` registers, and reports `Conflict` for one it did not
create, so apply the example only to an infra-plane cluster no `CCLInfraPlane` registers:

```bash
kubectl apply -f examples/quickstart/v1alpha1_cclclusterconfig.yaml
kubectl get cclclusterconfig cluster
kubectl get cclclusterconfig cluster -o jsonpath='{.status.conditions[0].message}'
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
make build   # manifests, CRDs, deepcopy, Go client, both binaries, both images, both charts, Python models
make load    # build each operator image and load it into its own cluster
make deploy  # helm install: cp into ccluster-cp, ip into ccluster-ip
```

To author CRs from Python instead of YAML, `python/` is a generated Pydantic package covering
both CRDs and the catalog's typed chart values; see [python/README.md](python/README.md).
Go programs can use the generated clientset, listers and informers in [`pkg/client/`](pkg/client/).

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

See [Development](docs/development.md) for controller wiring, RBAC, CRD and chart
generation, [Testing](docs/testing.md) for test selection, and
[Build and release contracts](docs/build-and-release.md) for Docker and CI mechanics.

# Requirements

- **Go:** 1.21 or newer. `make` builds with the toolchain pinned in `go.mod` and
  downloads it on first use.
- **Deployment targets (external):** a Kubernetes cluster and [Helm](https://helm.sh/).
  [Argo CD](https://argo-cd.readthedocs.io/) reconciles the charts and is installed into the
  control-plane cluster by `make deploy-cp`. GPU workloads additionally require the NVIDIA
  GPU Operator / drivers on the cluster.
- **Local development:** [kind](https://kind.sigs.k8s.io/), Docker and
  [uv](https://docs.astral.sh/uv/), which `make build` uses to regenerate the Python
  models. `make init` creates both development clusters; `make build load deploy` builds,
  loads and installs into them.

## Releases

Every merge to `main` publishes both operator images tagged with the commit hash and both
charts as a pre-release of the next version, `X.Y.Z-dev.<sha>` (for example
`0.0.2-dev.<sha>` while the latest release is `v0.0.1`), so any commit can be deployed
before it is released. The same merge publishes the matching client to PyPI as
`ccluster-py==X.Y.Z.dev<N>`, where `N` is the commit's position on `main`, because a
Python version cannot carry a hash:

```bash
make deploy-cp IMG_TAG=<sha>
helm install ccluster-cp oci://ghcr.io/nvidia/charts/ccluster-cp --version 0.0.2-dev.<sha> ...
pip install "ccluster-py==0.0.2.dev$(git rev-list --count <sha>)"
```

The commit for a published `.dev<N>` is in the file's provenance on PyPI and in the
summary of the `Main` workflow run that published it. `pip` ignores these pre-releases
unless pinned exactly, given `--pre`, or no release satisfies the requested range, so
`pip install ccluster-py` still installs the latest release but `ccluster-py>0.0.8`
installs a `0.0.9.dev<N>` when 0.0.8 is the latest release.

A release is a GitHub Release. Create one against a new tag `vX.Y.Z` (in the GitHub UI, or
`gh release create vX.Y.Z --generate-notes`) and `.github/workflows/release.yml` retags
the images already built for that commit as `X.Y.Z`, re-packages both charts as `X.Y.Z`
pointing at them, publishes `ccluster-py==X.Y.Z` to PyPI, and attaches the tarballs and
dists to the release. Nothing in the tree is bumped.

| Artifact | Location |
| --- | --- |
| Operator images | `ghcr.io/nvidia/ccluster-cp-operator:X.Y.Z`, `ghcr.io/nvidia/ccluster-ip-operator:X.Y.Z` (also tagged with the commit hash) |
| compute-ssh launcher image | `ghcr.io/nvidia/ccluster-compute-ssh:X.Y.Z` (also tagged with the commit hash); `compute_deployment` defaults to it |
| Helm charts | `oci://ghcr.io/nvidia/charts/ccluster-cp`, `oci://ghcr.io/nvidia/charts/ccluster-ip` |
| Python package | [`ccluster-py` on PyPI](https://pypi.org/project/ccluster-py/), imported as `ccluster` |
| Catalog | This repository at tag `vX.Y.Z`; the released cp chart defaults `--catalog-revision` to it |

- **Changelog:** [`CHANGELOG.md`](CHANGELOG.md)
- Broader direction and upcoming capabilities are described in [DESIGN.md](DESIGN.md).

# Contribution Guidelines

CCluster is early and evolving quickly — contributions, ideas, and feedback on the direction are welcome.

- Start here: [`CONTRIBUTING.md`](CONTRIBUTING.md)
- Working on the operators: [Development](docs/development.md) for build and generation
  mechanics, [`DESIGN.md`](DESIGN.md) for the control plane, and
  [`AGENTS.md`](AGENTS.md) for agent instructions.
- Development quickstart:

```bash
make build          # manifests, CRDs, deepcopy, Go client, both binaries, both images, both charts, Python models
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

The Python package under [`python/`](python/) is also licensed under Apache-2.0. Its
[`python/LICENSE`](python/LICENSE) is a symlink to the root [LICENSE](LICENSE), so the
Python build, which runs from `python/`, includes the root license in the sdist and wheel;
see [Build and release contracts](docs/build-and-release.md#license-payload). Third-party
components in the operator images and Helm charts, and the catalog charts' Helm
dependencies, are listed in [`THIRD_PARTY_NOTICES.md`](THIRD_PARTY_NOTICES.md).

# Notice and Disclaimer

NOTICE AND DISCLAIMER: This software automatically retrieves, accesses or interacts with external materials.  Those retrieved materials are not distributed with this software and are governed solely by separate terms, conditions and licenses.  You are solely responsible for finding, reviewing and complying with all applicable terms, conditions, and licenses, and for verifying the security, integrity and suitability of any retrieved materials for your specific use case. This software is provided "AS IS", without warranty of any kind. The author makes no representations or warranties regarding any retrieved materials, and assumes no liability for any losses, damages, liabilities or legal consequences from your use or inability to use this software or any retrieved materials. Use this software and the retrieved materials at your own risk.
