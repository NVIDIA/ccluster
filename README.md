# CCluster

**An efficiency-focused, Kubernetes-native platform for managing the end-to-end AI lifecycle across distributed, multi-cloud GPU infrastructure.**

CCluster aims to give AI teams a single control plane for their GPU fleet — provisioning clusters, deploying and scaling workloads, and running the full model lifecycle (inference, fine-tuning, batch jobs, and training) across clouds and regions, while keeping expensive accelerators as busy and as cost-effective as possible.

> **Project status: early / foundations.** CCluster is at the start of an ambitious roadmap. The repository today contains the first building block — a catalog of Helm charts and typed value schemas for cluster and workload deployment (see [Available today](#available-today)). The broader platform described below is the vision we're building toward, not yet shipped. Expect rapid, breaking change.

# Vision

GPU capacity is scarce, expensive, and increasingly spread across many clouds, regions, and on-prem sites. Teams end up stitching together bespoke tooling for each environment, and utilization suffers. CCluster's goal is to make a heterogeneous, multi-cloud GPU fleet feel like one efficient, Kubernetes-native platform.

Guiding principles:

- **Efficiency first.** Maximize accelerator utilization through bin-packing, autoscaling, and workload-aware scheduling — treating idle GPU time as the primary cost to eliminate.
- **Kubernetes-native.** Everything is expressed as Kubernetes resources and reconciled declaratively (GitOps via Argo CD), so the platform composes with the existing cloud-native ecosystem instead of replacing it.
- **Multi-cloud by default.** A cluster on AWS, GCP, Azure, Nebius, or on-prem is a first-class citizen; workloads and policy span providers without per-cloud rewrites.
- **End-to-end lifecycle.** One platform for the whole journey — from provisioning a cluster, to training and fine-tuning, to batch and interactive compute, to production inference and autoscaling.
- **Typed and validated.** Deployment configuration is modeled as typed schemas that are validated before anything reaches a cluster, so misconfiguration fails fast.

## Where we're headed

The platform is being built up in layers. The chart catalog is the foundation; higher layers (a control-plane API, a scheduler/placement layer, and user-facing interfaces) will build on top of it.

| Capability | Description | Status |
| --- | --- | --- |
| **Deployment catalog** | Helm charts + typed value schemas for cluster base components and user workloads (inference, compute, jobs). | ✅ Available today |
| **Cluster provisioning** | Declarative bring-up of GPU clusters and their platform stack across clouds. | 🧱 Foundations in place (`cluster_base`) |
| **Control-plane API** | Programmatic management of clusters, deployments, and users. | 🗺️ Planned |
| **Workload scheduling & placement** | Efficiency-aware scheduling and cross-cluster placement of GPU workloads. | 🗺️ Planned |
| **Multi-cloud fleet management** | Unified view and policy across clusters spanning multiple providers. | 🗺️ Planned |
| **Lifecycle tooling** | Training, fine-tuning, and batch pipelines alongside inference serving. | 🗺️ Planned |

*Status markers are directional and will evolve as the project matures.*

# Available today

The current codebase ships as a single Python package, `catalog`, that pairs each Helm chart with a [Pydantic](https://docs.pydantic.dev/) model describing its values. Instead of hand-written `values.yaml` files, callers construct a typed model and let the package emit Helm parameters or a `values.yaml` for [Argo CD](https://argo-cd.readthedocs.io/). This is the deployment substrate the rest of the platform will build on:

- **Type-safe Helm values** — chart inputs are validated Pydantic models, so misconfigured deployments fail at construction time rather than at sync time.
- **Argo CD-friendly rendering** — values are flattened into Argo CD `helm.parameters` (with correct `forceString` handling) or a values object, avoiding drift between Kubernetes and Argo CD (e.g. CPU/memory normalization, annotation-key escaping).
- **A composable cluster base** — `cluster_base` models a full platform stack (Istio, cert-manager, SPIRE, KEDA, GPU Operator, Prometheus, Longhorn, and more) as per-component overrides emitted into a single Argo CD `ApplicationSet`, ordered by sync waves.
- **Ready-to-deploy workload charts** — inference, CServe, compute, and job charts for running user workloads.

## What's in the catalog

| Chart | Purpose |
| --- | --- |
| `cluster_base` | Cluster-wide platform components (Istio, cert-manager, SPIRE, KEDA, GPU Operator, kube-prometheus-stack, Longhorn, external-dns, Fluent Bit, Argo Rollouts, and more), emitted as one Argo CD `ApplicationSet` per cluster. |
| `namespace_base` | Per-user namespace configuration: network policies, Istio sidecars, authorization/peer-authentication policies, service monitors, model-cache volumes. |
| `inference_deployment_v3` | Inference workload deployment using Argo Rollouts, with autoscaling (KEDA), ingress, and mTLS. |
| `cserve_deployment_v3` | CServe deployment using Argo Rollouts. |
| `compute_deployment` | Interactive compute deployment (e.g. SSH-accessible dev environments). |
| `job_deployment` | One-shot / batch job deployment with PVC creation and mounting. |

# Getting Started

The catalog is a Python package. Install it into the environment of the service or tooling that builds your Argo CD Applications.

```bash
# From a checkout of this repository
pip install ./catalog

# Or, for local development (editable install)
pip install -e ./catalog
```

The package bundles the Helm chart assets (templates, `Chart.yaml`, `.tpl`, `Chart.lock`) alongside the schemas, so charts can be resolved from disk at runtime — no separate chart download step is required.

# Requirements

- **Python:** >= 3.10 (uses `StrEnum` and modern typing syntax)
- **Runtime dependencies:** `pydantic`, `pyyaml` (installed automatically)
- **Deployment targets (external):** a Kubernetes cluster, [Argo CD](https://argo-cd.readthedocs.io/), and [Helm](https://helm.sh/) to render and reconcile the charts. GPU workloads additionally require the NVIDIA GPU Operator / drivers on the cluster.

# Usage

Define a typed values model and render it into Argo CD Helm parameters:

```python
from catalog.components import (
    DeploymentBaseHelmValues,
    ResourceLimitsRequests,
    Resources,
)

class InferenceValues(DeploymentBaseHelmValues):
    name: str
    image_url: str

values = InferenceValues(
    name="my-model",
    image_url="registry.example.com/my-model:latest",
    resources=ResourceLimitsRequests(
        limits=Resources(cpu="4", memory="16384Mi", accelerator_count=1),
        requests=Resources(cpu="2", memory="8192Mi", accelerator_count=1),
    ),
)

# Flattened Argo CD Helm parameters (with forceString handling)
params = values.helm_params()

# Or a values.yaml string for an Argo CD Application source
values_yaml = values.yaml()
```

To assemble a cluster's platform stack, build a `ClusterComponents` and serialize it with cluster context:

```python
from catalog.cluster_base import ClusterComponents, Shared

components = ClusterComponents()          # sensible per-component defaults
shared = Shared()                         # cluster identity, cloud provider, etc.

# Emits one ApplicationSet element per enabled component, with
# cluster-specific defaults injected under each component's `values`.
appset_elements = components.model_dump(by_alias=True, context={"shared": shared})
```

- Adding a new chart to the catalog: see [`catalog/README.md`](catalog/README.md).

## Releases

- **Changelog:** [`CHANGELOG.md`](CHANGELOG.md)
- Broader direction and upcoming capabilities are described in [Where we're headed](#where-were-headed).

# Contribution Guidelines

CCluster is early and evolving quickly — contributions, ideas, and feedback on the direction are welcome.

- Start here: [`CONTRIBUTING.md`](CONTRIBUTING.md)
- Adding or modifying charts and schemas: [`catalog/README.md`](catalog/README.md)
- Development quickstart (install and test):

```bash
pip install -e ./catalog
python -m unittest discover -s catalog -p "test_*.py"
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
