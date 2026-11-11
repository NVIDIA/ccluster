# CCluster

[![License](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](LICENSE)
[![Main](https://github.com/NVIDIA/ccluster/actions/workflows/main.yml/badge.svg?branch=main)](https://github.com/NVIDIA/ccluster/actions/workflows/main.yml)

**An efficiency-focused, Kubernetes-native platform for managing the end-to-end AI lifecycle across distributed, multi-cloud GPU infrastructure.**

CCluster aims to give AI teams a single control plane for their GPU fleet — provisioning clusters, deploying and scaling workloads, and running the full model lifecycle (inference, fine-tuning, batch jobs, and training) across clouds and regions, while keeping expensive accelerators as busy and as cost-effective as possible.

> **Project status: early.** What exists today is the first working slice: a control plane
> that registers Kubernetes clusters and rolls out ordered sets of Helm charts onto them
> through Argo CD, plus the catalog of charts it deploys. The broader platform described
> below is the vision we're building toward. Expect rapid, breaking change.

## Vision

GPU capacity is scarce, expensive, and increasingly spread across many clouds, regions, and on-prem sites. Teams end up stitching together bespoke tooling for each environment, and utilization suffers. CCluster's goal is to make a heterogeneous, multi-cloud GPU fleet feel like one efficient, Kubernetes-native platform.

Guiding principles:

- **Efficiency first.** Maximize accelerator utilization through bin-packing, autoscaling, and workload-aware scheduling — treating idle GPU time as the primary cost to eliminate.
- **Kubernetes-native.** Everything is expressed as Kubernetes resources and reconciled declaratively (GitOps via Argo CD), so the platform composes with the existing cloud-native ecosystem instead of replacing it.
- **Multi-cloud by default.** A cluster on AWS, GCP, Azure, Nebius, or on-prem is a first-class citizen; workloads and policy span providers without per-cloud rewrites.
- **End-to-end lifecycle.** One platform for the whole journey — from provisioning a cluster, to training and fine-tuning, to batch and interactive compute, to production inference and autoscaling.
- **Secure and isolated.** Teams share one control plane without sharing access: each team's clusters, credentials and deployments are confined to its own namespace, and every component runs with only the permissions its job needs.

## How it works

One **control-plane cluster** manages any number of **infra-plane clusters**, the clusters
that run your workloads, on any cloud or on-prem:

```
                        control-plane cluster
    ┌────────────────────────────────────────────────────────┐
    │                                                        │
    │   ccluster-cp-operator ─────▶ Argo CD ◀────────────────┼─── catalog
    │        (the brain)                                     │    (Helm charts)
    └─────────────┬──────────────────────────┬───────────────┘
                  │                          │
        deploys ▼ │ ▲ status       deploys ▼ │ ▲ status
                  │                          │
    ┌─────────────┴────────────┐ ┌───────────┴──────────────┐
    │ ccluster-ip-operator     │ │ ccluster-ip-operator     │
    │   (verifies, reports)    │ │   (verifies, reports)    │
    │ platform components      │ │ platform components      │
    │ workloads                │ │ workloads                │
    └──────────────────────────┘ └──────────────────────────┘
       infra-plane cluster A        infra-plane cluster B
```

- **The control-plane operator is the brain.** It holds every registered cluster's
  credentials, decides what is deployed where and in what order, drives Argo CD to deploy
  it, and collects each cluster's status in one place.
- **The infra-plane operator verifies and reports.** It runs in each infra-plane cluster,
  checks that the platform components the cluster needs (the GPU Operator, cert-manager,
  Istio, KEDA and others) are installed and healthy, and reports the result back to the
  control plane. It never installs or changes anything itself.
- **The catalog** is the set of Helm charts CCluster deploys, from those platform
  components to inference, training and compute workloads. It lives in this repository.

The lifecycle:

1. **Install** the control-plane operator in one cluster and the infra-plane operator in
   each cluster you want to manage.
2. **Register** each infra-plane cluster with the control plane, in a namespace that
   belongs to one team. The control plane gives Argo CD access to the cluster and gives it
   a DNS domain under the control plane's base domain.
3. **Set up the platform components** in one of two modes:
   - **Auto-mode:** the control plane installs them, deploying the catalog's platform
     charts onto the cluster through Argo CD like any other component.
   - **Self-mode:** you install them yourself with Helm, and the control plane deploys only
     your workloads.

   Either way, the infra-plane operator checks the result the same way.
4. **Deploy workloads** as ordered sets of catalog charts. The control plane rolls them out
   one at a time, each waiting for the one before it to be healthy.
5. **Watch status** from the control plane: rollout progress for every set, and each
   cluster's verification result.

[DESIGN.md](DESIGN.md) describes how each step works.

# Getting started

## Install

You need Kubernetes clusters for the control plane and each infra plane (one cluster can play
both roles to try it out), [Helm](https://helm.sh/) 3, and a DNS domain for the control plane,
the *base domain*. Pick a [release](https://github.com/NVIDIA/ccluster/releases) `X.Y.Z`.

**Control-plane cluster** — the operator and Argo CD:

```bash
helm install ccluster-cp oci://ghcr.io/nvidia/charts/ccluster-cp --version X.Y.Z \
  -n ccluster-cp-system --create-namespace \
  --set-json 'manager.args=["--leader-elect","--catalog-repo-url=https://github.com/NVIDIA/ccluster","--catalog-revision=vX.Y.Z","--argocd-server-service=ccluster-cp-argocd-server","--base-domain=planes.example.com"]'
```

**Each infra-plane cluster:**

```bash
helm install ccluster-ip oci://ghcr.io/nvidia/charts/ccluster-ip --version X.Y.Z \
  -n ccluster-ip-system --create-namespace
```

**The command line**, with Go or Python (the Python package includes the SDK):

```bash
go install github.com/NVIDIA/ccluster/cmd/ccluster@vX.Y.Z
# or
pip install ccluster-py
```

Prebuilt binaries for Linux and macOS are on each GitHub Release.
[Installation](docs/installation.md) covers the chart options, using an existing Argo CD,
publishing DNS records, development builds, upgrading and uninstalling.

## Register a cluster

Point the CLI at the control plane, then register an infra-plane cluster, both by their
kubeconfig contexts:

```bash
ccluster init --control-plane <control-plane context>
ccluster register edge-1 -n team-a --infra-plane <infra-plane context>
```

This registers the cluster as `edge-1` in namespace `team-a`. A namespace is a team's
boundary: what is registered and deployed in one is invisible to another.
`ccluster unregister edge-1 -n team-a` removes the registration. See
[Installation: command line](docs/installation.md#command-line) for what `register` creates
and for shell completion.

## Quickstart: deploy an inference service

With a cluster registered as `edge-1` in `team-a`, deploy an embeddings service
([text-embeddings-inference](https://github.com/huggingface/text-embeddings-inference)
serving `BAAI/bge-small-en-v1.5`, which runs on CPU) using the Python SDK. The cluster
needs to support LoadBalancer Services, as managed Kubernetes on any cloud does.

**1. Set up the platform components (auto-mode).** List them in a component set; the
control plane rolls them out in order, each waiting for the one before to be healthy:

```python
from kubernetes.config import new_client_from_config
from ccluster import catalogs
from ccluster.catalogs.cluster_base import (
    argo_rollouts, ingress_nginx, istio, keda, kube_prometheus_stack,
)
from ccluster.catalogs.deployments import inference_deployment as inference
from ccluster.client import CCLClient

client = CCLClient("team-a", api_client=new_client_from_config(context="<control-plane context>"))
client.apply_component_set(name="platform", components=[
    catalogs.component("kube-prometheus-stack", "edge-1", kube_prometheus_stack.CHART, "prometheus"),
    catalogs.component("istio", "edge-1", istio.CHART, "istio-system"),
    catalogs.component("ingress-nginx", "edge-1", ingress_nginx.CHART, "ingress-nginx"),
    catalogs.component("keda", "edge-1", keda.CHART, "keda"),
    catalogs.component("argo-rollouts", "edge-1", argo_rollouts.CHART, "argo-rollouts"),
])
```

**2. Deploy the service** once `client.is_component_set_finished(name="platform")` returns
`True`. Every catalog chart has typed values:

```python
domain = client.get_infraplane(name="edge-1").status.domain
size = inference.Resources(cpu="2", memory="4Gi")
client.apply_component_set(name="embeddings", components=[
    catalogs.component("embeddings", "edge-1", inference.CHART, "embeddings", inference.Values(
        name="embeddings",
        deployment_id="embeddings",
        image_url="ghcr.io/huggingface/text-embeddings-inference:cpu-1.8",
        args=["--model-id", "BAAI/bge-small-en-v1.5", "--port", "8080"],
        container_port=8080,
        healthcheck="/health",
        domain=domain,
        resources=inference.ResourceRequirements(limits=size, requests=size),
        scheduler_name="",  # the default scheduler; kai-scheduler is not installed here
    )),
])
```

**3. Call it** at `https://embeddings.nginx.<domain>` through the cluster's ingress:

```bash
curl -k https://embeddings.nginx.<domain>/embed --resolve embeddings.nginx.<domain>:443:<ingress IP> \
  -H 'Content-Type: application/json' -d '{"inputs": "Hello, CCluster"}'
```

`kubectl -n ingress-nginx get svc` on the infra plane shows the ingress IP. Until DNS is
published for the cluster (see [Installation: DNS](docs/installation.md#dns)), `--resolve`
stands in for it, and `-k` accepts the ingress's default certificate.

[`examples/quickstart`](examples/quickstart/) has the whole walkthrough as runnable
quickstarts for the Python SDK, the Go SDK and Helm, from registering the cluster to
calling the service and cleaning up. For a GPU model, set `accelerator_count` in the
resources (the key defaults to `nvidia.com/gpu`) and add `gpu_operator` to the platform
set. The [Python README](python/README.md) covers the full SDK; Go programs can use
[`pkg/cclclient`](pkg/cclclient/), which offers the same operations.

# Developing CCluster

The development setup runs the two operators in a pair of local
[kind](https://kind.sigs.k8s.io/) clusters, `ccluster-cp` and `ccluster-ip`. You need Go
1.21 or newer (`make` downloads the toolchain `go.mod` pins), Docker,
[Helm](https://helm.sh/) and [uv](https://docs.astral.sh/uv/). `make` downloads its other
tools into `bin/`.

**Make targets act on your current kubeconfig context without asking**, and kind changes
the current context. If your kubeconfig holds other clusters, pin a dedicated one first:

```bash
export KUBECONFIG=~/.kube/ccluster-dev.yaml
```

Create the clusters, build everything from your checkout, and deploy:

```bash
make init     # create both kind clusters
make build    # generated code, binaries, images, charts and Python models
make load     # load the images into the kind clusters
make deploy   # install each operator's chart into its cluster
```

Then apply the [single-cluster examples](examples/quickstart/kubectl/), which register the
control-plane cluster with itself, or build the CLI with `make build-cli` and register
the infra-plane cluster:

```bash
bin/ccluster init --control-plane kind-ccluster-cp
bin/ccluster register edge-1 -n team-a --infra-plane kind-ccluster-ip \
    --server https://ccluster-ip-control-plane:6443
```

Useful targets while iterating:

| Command | What it does |
| --- | --- |
| `make test` | Unit and envtest suites; no cluster needed |
| `make load-cp deploy-cp` | Rebuild and redeploy one operator (`-cp` or `-ip`) |
| `make run-cp` | Run the control-plane operator on your host against the cluster |
| `make test-e2e` | End-to-end tests in their own pair of kind clusters |
| `make pre-commit` | Every check to run before a commit: tidy, generate, lint, test |
| `make clean` | Delete both kind clusters |
| `make help` | List every target |

Further reading:

- [DESIGN.md](DESIGN.md): the API, reconciliation, ordering and tenancy, and why.
- [Development](docs/development.md): adding controllers, APIs and catalog components;
  code generation, RBAC and charts; cluster operations.
- [Testing](docs/testing.md): choosing a test layer and running e2e.
- [Build and release contracts](docs/build-and-release.md): images, CI and publishing.
- [CONTRIBUTING.md](CONTRIBUTING.md): sign-off and pull requests.
- [AGENTS.md](AGENTS.md): instructions for coding agents.

# Releases

Releases are published as [GitHub Releases](https://github.com/NVIDIA/ccluster/releases) and
listed in [CHANGELOG.md](CHANGELOG.md). Each one publishes:

| Artifact | Location |
| --- | --- |
| Helm charts | `oci://ghcr.io/nvidia/charts/ccluster-cp`, `oci://ghcr.io/nvidia/charts/ccluster-ip` |
| Operator images | `ghcr.io/nvidia/ccluster-cp-operator`, `ghcr.io/nvidia/ccluster-ip-operator` |
| Command line | Release tarballs, or `go install github.com/NVIDIA/ccluster/cmd/ccluster@vX.Y.Z` |
| Python SDK | [`ccluster-py` on PyPI](https://pypi.org/project/ccluster-py/) |
| Catalog | This repository at tag `vX.Y.Z` |

Every merge to `main` also publishes development builds; see
[Installation: development builds](docs/installation.md#development-builds).

# Community

- **Questions and ideas:** [GitHub Discussions](https://github.com/NVIDIA/ccluster/discussions)
  or [Issues](https://github.com/NVIDIA/ccluster/issues); see [SUPPORT.md](SUPPORT.md).
- **Contributing:** [CONTRIBUTING.md](CONTRIBUTING.md). Maintainers are listed in
  [MAINTAINERS.md](MAINTAINERS.md), and the [Code of Conduct](CODE_OF_CONDUCT.md) applies.
- **Security:** report vulnerabilities as described in [SECURITY.md](SECURITY.md), not in a
  public issue.

# License

CCluster, including the Python package, is licensed under the [Apache License 2.0](LICENSE).
Third-party components in the images and charts, and the catalog charts' dependencies, are
listed in [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).

# Notice and Disclaimer

NOTICE AND DISCLAIMER: This software automatically retrieves, accesses or interacts with external materials.  Those retrieved materials are not distributed with this software and are governed solely by separate terms, conditions and licenses.  You are solely responsible for finding, reviewing and complying with all applicable terms, conditions, and licenses, and for verifying the security, integrity and suitability of any retrieved materials for your specific use case. This software is provided "AS IS", without warranty of any kind. The author makes no representations or warranties regarding any retrieved materials, and assumes no liability for any losses, damages, liabilities or legal consequences from your use or inability to use this software or any retrieved materials. Use this software and the retrieved materials at your own risk.
