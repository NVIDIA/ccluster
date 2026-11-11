# Installation and usage

This guide installs released versions of CCluster and covers the command line in full. To
build and run CCluster from a checkout instead, see the
[README's development section](../README.md#developing-ccluster) and
[Development](development.md).

A CCluster installation has one **control-plane cluster** and any number of **infra-plane
clusters**:

- The control-plane cluster runs `ccluster-cp-operator` and Argo CD. You register infra-plane
  clusters with it and tell it which components to deploy onto them; it deploys them through
  Argo CD.
- Each infra-plane cluster runs `ccluster-ip-operator`, which checks that the platform
  components the cluster needs are present and reports back. It installs nothing itself.

[DESIGN.md](../DESIGN.md) explains the API and how rollout works.

## Prerequisites

- Kubernetes clusters for the control plane and for each infra plane. One cluster can play
  both roles for a trial.
- [Helm](https://helm.sh/) 3 and `kubectl`, with a kubeconfig context for each cluster.
- Network reachability from the control-plane cluster to each infra-plane cluster's API
  server: Argo CD and the control-plane operator both connect to it.
- A DNS domain for the control plane, the *base domain*. Each registered cluster gets a
  domain under it, `<name>.<namespace>.<base domain>`. Publishing DNS records for it is
  optional; see [DNS](#dns).
- GPU workloads additionally need the NVIDIA GPU Operator, which the catalog's
  `cluster_base/gpu_operator` component deploys.

Every cluster command acts on your current kubeconfig context without asking. Pin the
intended cluster first, with `--kube-context` or a dedicated `KUBECONFIG`.

## Control plane

The `ccluster-cp` chart installs the operator and, as a dependency, Argo CD:

```bash
helm install ccluster-cp oci://ghcr.io/nvidia/charts/ccluster-cp --version X.Y.Z \
  -n ccluster-cp-system --create-namespace \
  --set-json 'manager.args=["--leader-elect","--catalog-repo-url=https://github.com/NVIDIA/ccluster","--catalog-revision=vX.Y.Z","--argocd-server-service=ccluster-cp-argocd-server","--base-domain=planes.example.com"]'
```

Replace `planes.example.com` with your base domain and `X.Y.Z` with a
[release](https://github.com/NVIDIA/ccluster/releases).

- `--base-domain` is required; the operator does not start without it. Set it once: changing
  it moves every registered cluster's domain. Two control planes must not share one.
- Setting `manager.args` replaces the chart's whole list, so the command repeats the chart's
  other arguments. `helm show values oci://ghcr.io/nvidia/charts/ccluster-cp --version X.Y.Z`
  prints them.
- `--catalog-repo-url` and `--catalog-revision` name the Git repository and revision Argo
  CD deploys catalog charts from. A released chart pins the revision to its own `vX.Y.Z`
  tag; leave it out and the operator deploys the catalog from `main`. Point them at a fork
  or branch to deploy your own charts. Argo CD clones the repository itself, so the
  revision must be pushed, and a private fork needs its credentials configured in Argo CD.
- `--argocd-server-service` names the bundled Argo CD's API Service, which the operator
  reads Applications' health through.
- On every `helm upgrade`, change `--version` and `--catalog-revision` together.
  `--reuse-values` keeps the old revision.

`charts/cp/chart/values.yaml` documents every other value and operator flag.

### Existing Argo CD

To use an Argo CD already running in the control-plane cluster, install with
`--set argo-cd.enabled=false` and add `--argocd-namespace=<its namespace>` to
`manager.args`. The operator then needs a read-only Argo CD account and an API token; see
[Development: Argo CD API token](development.md#argo-cd-api-token) for the steps.

### DNS

The operator writes a DNS record request for each registered cluster's load balancers, such
as `*.nginx.<cluster domain>` for its ingress. Nothing is published unless the chart's
bundled external-dns is enabled and configured for your DNS provider and zone; see
[Development: DNS and external-dns](development.md#dns-and-external-dns).

## Infra planes

Install the `ccluster-ip` chart in each infra-plane cluster:

```bash
helm install ccluster-ip oci://ghcr.io/nvidia/charts/ccluster-ip --version X.Y.Z \
  -n ccluster-ip-system --create-namespace
```

Then register the cluster with the control plane using the command line below.

## Command line

The `ccluster` command registers infra-plane clusters with a control plane.

### Install

Use one of:

```bash
# Go 1.21 or newer, which downloads the toolchain go.mod pins
go install github.com/NVIDIA/ccluster/cmd/ccluster@vX.Y.Z

# Python; also installs the SDK
pip install ccluster-py
```

or download `ccluster_X.Y.Z_<os>_<arch>.tar.gz` for Linux or macOS on amd64 or arm64 from
the [GitHub Release](https://github.com/NVIDIA/ccluster/releases) and put the `ccluster`
binary on your `PATH`. The Go and Python builds take the same subcommands, long flags and
config file; the Python one also accepts the short forms `-cp` and `-ip`. Install only one:
both are called `ccluster`, and the first on your `PATH` wins.

The macOS binaries are not yet signed or notarized, so macOS refuses to run one downloaded
with a browser. Download the tarball with `curl`, which does not quarantine it, or clear
the quarantine before the first run:

```bash
curl -fsSL https://github.com/NVIDIA/ccluster/releases/download/vX.Y.Z/ccluster_X.Y.Z_darwin_arm64.tar.gz | tar -xz
# or, after unpacking a browser download:
xattr -d com.apple.quarantine ccluster_X.Y.Z_darwin_arm64/ccluster
```

Use `darwin_amd64` on an Intel Mac.

### Choose a control plane

```bash
ccluster init --control-plane <control-plane context>
```

`init` checks that the context's cluster serves the CCluster API and saves the context name
to `~/.config/ccluster/config.yaml` (`$XDG_CONFIG_HOME/ccluster/config.yaml` if set, or the
file named by `$CCLUSTER_CONFIG`). Only the name is saved; credentials stay in the
kubeconfig, found through `$KUBECONFIG` or `~/.kube/config` as `kubectl` finds it. Run it
again to switch control planes.

### Register a cluster

```bash
ccluster register edge-1 -n tenant-a --infra-plane <infra-plane context>
```

This registers the cluster as `edge-1` in the control plane's namespace `tenant-a`, creating
the namespace if needed. Namespaces are the isolation boundary: clusters and deployments in
one namespace are not visible to another. The cluster's domain is
`edge-1.tenant-a.<base domain>`.

On the infra plane, `register` creates the ServiceAccount `kube-system/ccluster-manager`,
binds it to `cluster-admin` (Argo CD installs CRDs and other cluster-scoped objects there,
so it needs the same rights `argocd cluster add` grants), and reads its long-lived token. On
the control plane it stores that token and the cluster CA in the Secret
`edge-1-credentials`. **Anyone who can read that Secret is cluster-admin on the infra
plane**, so keep read access to the namespace's Secrets tight. Running `register` again
reuses the same token.

`--server` is the address the control plane dials, and defaults to the infra-plane
context's server. Set it when that address is only reachable from your workstation, as with
kind, whose kubeconfig points at `127.0.0.1`.

### Unregister a cluster

```bash
ccluster unregister edge-1 -n tenant-a
```

This removes the registration and its credentials Secret from the control plane, and leaves
the namespace. The infra plane is not touched; its ServiceAccount and token stay, so
registering the cluster again reuses them.

### Shell completion

```bash
# bash (needs the bash-completion package)
echo 'eval "$(ccluster completion bash)"' >> ~/.bashrc

# zsh (the first line only if completion is not enabled yet)
echo 'autoload -U compinit; compinit' >> ~/.zshrc
echo 'eval "$(ccluster completion zsh)"' >> ~/.zshrc

# fish
ccluster completion fish > ~/.config/fish/completions/ccluster.fish
```

Start a new shell for the change to take effect.

## Deploying components

Components are Helm charts from the catalog in [`catalogs/`](../catalogs/), deployed onto
registered clusters in the order you list them. Deploy them with an SDK:

- **Python:** `pip install ccluster-py`. `CCLClient` registers clusters and applies, polls
  and deletes component sets, with typed values for every catalog chart. See the
  [Python README](../python/README.md).
- **Go:** [`pkg/cclclient`](../pkg/cclclient/) offers the same operations; the generated
  clientset, listers and informers are in [`pkg/client`](../pkg/client/).

The [quickstarts](../examples/quickstart/) register a cluster and deploy an inference
service with the Python SDK, the Go SDK and Helm. [DESIGN.md](../DESIGN.md) describes each
field of the underlying resources.

## Development builds

Every merge to `main` publishes both operator images tagged with the commit hash, both
charts as a pre-release of the next version, `X.Y.Z-dev.<sha>`, and the Python package as
`ccluster-py==X.Y.Z.dev<N>`, where `N` is the commit's position on `main`
(`git rev-list --count <sha>`):

```bash
helm install ccluster-cp oci://ghcr.io/nvidia/charts/ccluster-cp --version 0.0.2-dev.<sha> ...
pip install "ccluster-py==0.0.2.dev$(git rev-list --count <sha>)"
```

`pip` ignores these pre-releases unless pinned exactly or given `--pre`. The CLI binaries
are built only for releases. [Build and release contracts](build-and-release.md#artifact-versions-and-publishing)
has the details.

## Uninstalling

While the operators still run, delete your component sets, which uninstalls their charts
from the infra planes in reverse order, then unregister the clusters. Then `helm uninstall`
each chart. The charts keep their CRDs; deleting a CRD deletes every remaining resource of
its kind.
