# CCluster

**An efficiency-focused, Kubernetes-native platform for managing the end-to-end AI lifecycle across distributed, multi-cloud GPU infrastructure.**

CCluster aims to give AI teams a single control plane for their GPU fleet — provisioning clusters, deploying and scaling workloads, and running the full model lifecycle (inference, fine-tuning, batch jobs, and training) across clouds and regions, while keeping expensive accelerators as busy and as cost-effective as possible.

> **Project status: early.** CCluster is at the start of an ambitious roadmap. What exists today is the first working slice of the control plane — two `v1alpha1` CRDs that register clusters with Argo CD and roll out ordered bundles of Helm charts onto them (see [Available today](#available-today)) — plus the chart catalog they deploy from. The broader platform described below is the vision we're building toward, not yet shipped. Expect rapid, breaking change.

# Vision

GPU capacity is scarce, expensive, and increasingly spread across many clouds, regions, and on-prem sites. Teams end up stitching together bespoke tooling for each environment, and utilization suffers. CCluster's goal is to make a heterogeneous, multi-cloud GPU fleet feel like one efficient, Kubernetes-native platform.

Guiding principles:

- **Efficiency first.** Maximize accelerator utilization through bin-packing, autoscaling, and workload-aware scheduling — treating idle GPU time as the primary cost to eliminate.
- **Kubernetes-native.** Everything is expressed as Kubernetes resources and reconciled declaratively (GitOps via Argo CD), so the platform composes with the existing cloud-native ecosystem instead of replacing it.
- **Multi-cloud by default.** A cluster on AWS, GCP, Azure, Nebius, or on-prem is a first-class citizen; workloads and policy span providers without per-cloud rewrites.
- **End-to-end lifecycle.** One platform for the whole journey — from provisioning a cluster, to training and fine-tuning, to batch and interactive compute, to production inference and autoscaling.
- **Typed and validated.** Deployment configuration is modeled as CRD schemas with server-side validation, so misconfiguration is rejected by the API server rather than discovered at sync time.

## Where we're headed

The platform is being built up in layers. The chart catalog and the control-plane API are the foundation; higher layers (a scheduler/placement layer, fleet policy, and user-facing interfaces) will build on top of them.

| Capability | Description | Status |
| --- | --- | --- |
| **Deployment catalog** | Helm charts for cluster base components and user workloads (inference, compute, jobs). | ✅ Available today |
| **Control-plane API** | Kubernetes CRDs for registering clusters and rolling out chart bundles onto them. | ✅ Available today (`v1alpha1`) |
| **Cluster provisioning** | Declarative bring-up of GPU clusters and their platform stack across clouds. | 🧱 Foundations in place (`cluster_base` + `CCLDeployment`) |
| **Workload scheduling & placement** | Efficiency-aware scheduling and cross-cluster placement of GPU workloads. | 🗺️ Planned |
| **Multi-cloud fleet management** | Unified view and policy across clusters spanning multiple providers. | 🗺️ Planned |
| **Lifecycle tooling** | Training, fine-tuning, and batch pipelines alongside inference serving. | 🗺️ Planned |

*Status markers are directional and will evolve as the project matures.*

# Available today

CCluster runs as **two Kubernetes operators** and a **catalog of Helm charts**.

The control-plane operator (`ccluster-cp-operator`) runs in one control-plane cluster
alongside [Argo CD](https://argo-cd.readthedocs.io/), and manages a fleet of *infra-plane*
clusters. It installs nothing itself: it translates two custom resources into Argo CD API
objects and lets Argo CD do the work. The infra-plane operator (`ccluster-ip-operator`) runs
in each managed cluster and is still a placeholder.

[DESIGN.md](DESIGN.md) covers how this works and why — the ordering algorithm, the ownership
model, and the alternatives that were tried and rejected.

## The control-plane API

Two CRDs, both in the group `ccluster.nvidia.com/v1alpha1`.

### `CCLInfraPlane` — register a cluster

Points at a `Secret` holding a cluster's credentials. The operator mirrors it into the
Argo CD namespace as an Argo CD cluster Secret, under the `CCLInfraPlane`'s own name, so
deployments can address the cluster by that name.

```yaml
apiVersion: ccluster.nvidia.com/v1alpha1
kind: CCLInfraPlane
metadata:
  name: ccluster-local
spec:
  clusterCredentialsRef:
    name: ccluster-local-credentials
```

It reports `Available` once the cluster is registered. See
[`config/samples/v1alpha1_cclinfraplane.yaml`](config/samples/v1alpha1_cclinfraplane.yaml)
for a complete example, including registering the control-plane cluster with itself.

### `CCLDeployment` — roll out an ordered bundle of charts

A list of components, each one Helm chart from the catalog, on one registered cluster, in
one namespace, with its own values. **The list order is the rollout order:** the operator
creates a component's Argo CD `Application` only once every component before it reports
`Healthy` and `Synced`, and tears them down in reverse.

```yaml
apiVersion: ccluster.nvidia.com/v1alpha1
kind: CCLDeployment
metadata:
  name: ccluster-local-base
spec:
  components:
    - name: argo-rollouts
      infraPlaneRef: { name: ccluster-local }
      chart: catalogs/cluster_base/argo_rollouts
      namespace: argo-rollouts
    - name: keda
      infraPlaneRef: { name: ccluster-local }
      chart: catalogs/cluster_base/keda
      namespace: keda
      values:
        keda:
          operator:
            replicaCount: 1
```

`chart` is a path within the catalog Git repository, which the operator is configured with
(`--catalog-repo-url`, `--catalog-revision`) — so a chart change has to be *pushed* before
Argo CD sees it. `values` is forwarded to Helm untouched.

`status.conditions` reports `Validated`, `ComponentsApplied` and `Progressing`, the last of
which names whichever component the rollout is currently waiting on.

```bash
kubectl get ccldeployments
kubectl get applications -n argocd -L ccluster.nvidia.com/wave
```

## The chart catalog

`catalogs/` holds the Helm charts a `CCLDeployment` component can name.

| Chart | Purpose |
| --- | --- |
| `catalogs/cluster_base/*` | Cluster-wide platform components — Istio, cert-manager, SPIRE, KEDA, GPU Operator, kube-prometheus-stack, Longhorn, external-dns, Fluent Bit, Argo Rollouts, OpenTelemetry, Teleport and more. Each wraps an upstream chart with CCluster defaults. |
| `namespace_base` | Per-user namespace configuration: network policies, Istio sidecars, authorization/peer-authentication policies, service monitors, model-cache volumes. |
| `inference_deployment_v3` | Inference workload deployment using Argo Rollouts, with autoscaling (KEDA), ingress, and mTLS. |
| `cserve_deployment_v3` | CServe deployment using Argo Rollouts. |
| `compute_deployment` | Interactive compute deployment (e.g. SSH-accessible dev environments). |
| `job_deployment` | One-shot / batch job deployment with PVC creation and mounting. |

The workload charts still live under the older `catalog/` tree and are being migrated into
`catalogs/`, after which `catalog/` will be removed.

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
kubectl apply -f config/samples/v1alpha1_cclinfraplane.yaml

# Roll out an ordered bundle of catalog charts onto it
kubectl apply -f config/samples/v1alpha1_ccldeployment.yaml

kubectl get cclinfraplanes
kubectl get ccldeployments
kubectl get applications -n argocd -L ccluster.nvidia.com/wave
```

The sample `CCLDeployment` deploys from `--catalog-repo-url` at `--catalog-revision`
(`https://github.com/NVIDIA/ccluster` at `main` by default), so it works against a fresh
clone without any extra setup. Point it at a fork or branch with
`make deploy-cp CATALOG_REPO_URL=... CATALOG_REVISION=...`.

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
- Broader direction and upcoming capabilities are described in [Where we're headed](#where-were-headed).

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
