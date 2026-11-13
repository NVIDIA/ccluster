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
- [Helm](https://helm.sh/) 3 or 4 and `kubectl`, with a kubeconfig context for each cluster.
- Network reachability from the control-plane cluster to each infra-plane cluster's API
  server: Argo CD and the control-plane operator both connect to it.
- A DNS domain for the control plane, the *base domain*. Each registered cluster gets a
  domain under it, `<name>.<namespace>.<base domain>`. The operators run without publishing
  DNS records for it, but HTTPS endpoints such as the quickstart's need them; see
  [DNS](#dns).
- GPU workloads additionally need the NVIDIA GPU Operator, which the catalog's
  `cluster_base/gpu_operator` component deploys.

Every cluster command acts on your current kubeconfig context without asking. Pin the
intended cluster first, with `--kube-context` or a dedicated `KUBECONFIG`.

## Control plane

The `ccluster-cp` chart installs the operator and, as a dependency, Argo CD:

```bash
helm install ccluster-cp oci://ghcr.io/nvidia/charts/ccluster-cp --version X.Y.Z \
  -n ccluster-cp-system --create-namespace \
  --set manager.envOverrides.CCLUSTER_BASE_DOMAIN=planes.example.com
```

Replace `planes.example.com` with your base domain and `X.Y.Z` with a
[release](https://github.com/NVIDIA/ccluster/releases).

The operator is configured through environment variables, one per setting, named after
its flags: `--catalog-revision` reads `CCLUSTER_CATALOG_REVISION`. Set them with
`--set manager.envOverrides.<variable>=<value>`; each `--set` changes one variable and
keeps the chart's others. A flag in `manager.args` takes precedence over its variable.

- `CCLUSTER_BASE_DOMAIN` is required; the operator does not start without it. Set it
  once: changing it moves every registered cluster's domain. Two control planes must not
  share one.
- `CCLUSTER_CATALOG_REPO_URL` and `CCLUSTER_CATALOG_REVISION` name the Git repository and
  revision Argo CD deploys catalog charts from. A released chart sets them to this
  repository at its own `vX.Y.Z` tag. Point them at a fork or branch to deploy your own
  charts. Argo CD clones the repository itself, so the revision must be pushed, and a
  private fork needs its credentials configured in Argo CD.
- Upgrade with `helm upgrade --version <new version>` and the same `--set` flags. The new
  chart brings its own catalog revision; `--reuse-values` would keep the previous
  release's, and with it the previous catalog.

`charts/cp/chart/values.yaml` documents every variable, and `ccluster-cp-operator --help`
lists them with their flags.

### Existing Argo CD

To use an Argo CD already running in the control-plane cluster, install with
`--set argo-cd.enabled=false`, and point the operator at its namespace and its
argocd-server Service (default `argocd-server`):

```bash
  --set manager.envOverrides.CCLUSTER_ARGOCD_NAMESPACE=<argocd-namespace> \
  --set manager.envOverrides.CCLUSTER_ARGOCD_SERVER_SERVICE=<argocd-server-service>
```

The operator then needs a read-only Argo CD account and an API token; see
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

Download the binary for your platform, Linux or macOS on amd64 or arm64, from the
[GitHub Release](https://github.com/NVIDIA/ccluster/releases):

```bash
VERSION=X.Y.Z
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m | sed 's/x86_64/amd64/; s/aarch64/arm64/')
curl -fsSL "https://github.com/NVIDIA/ccluster/releases/download/v${VERSION}/ccluster_${VERSION}_${OS}_${ARCH}.tar.gz" | tar -xz
sudo install "ccluster_${VERSION}_${OS}_${ARCH}/ccluster" /usr/local/bin/
```

The tarball also carries the license and third-party notices. The macOS binaries are not
yet signed or notarized. `curl` does not quarantine what it downloads, so the commands
above work, but macOS refuses to run a binary downloaded with a browser until its
quarantine is cleared:

```bash
xattr -d com.apple.quarantine ccluster_X.Y.Z_darwin_arm64/ccluster
```

Alternatively:

```bash
# Go 1.21 or newer, which downloads the toolchain go.mod pins
go install github.com/NVIDIA/ccluster/cmd/ccluster@vX.Y.Z

# Python; also installs the SDK
pip install ccluster-py
```

The Go and Python builds take the same subcommands, long flags and config file; the Python
one also accepts the short forms `-cp` and `-ip`. Install only one: both are called
`ccluster`, and the first on your `PATH` wins.

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
ccluster register edge-1 -n team-a --infra-plane <infra-plane context>
```

This registers the cluster as `edge-1` in the control plane's namespace `team-a`, creating
the namespace if needed. Namespaces are the isolation boundary: clusters and deployments in
one namespace are not visible to another. The cluster's domain is
`edge-1.team-a.<base domain>`.

On the infra plane, `register` creates the ServiceAccount `kube-system/ccluster-manager`,
binds it to `cluster-admin` (Argo CD installs CRDs and other cluster-scoped objects there,
so it needs the same rights `argocd cluster add` grants), and reads its long-lived token. On
the control plane it stores that token and the cluster CA in the Secret
`edge-1-credentials`. **Anyone who can read that Secret is cluster-admin on the infra
plane**, so keep read access to the namespace's Secrets tight. Running `register` again
reuses the same token.

`--server` is the address the control plane dials, and defaults to the infra-plane
context's server. Set it when that address is only reachable from your workstation, as with
kind, whose kubeconfig points at `127.0.0.1`: use `https://kubernetes.default.svc` when the
infra plane is the control-plane cluster itself, and
`https://<kind cluster name>-control-plane:6443` for another kind cluster.

`kubectl get cclinfraplane -n team-a` shows `CLUSTERCONFIG True` once the control plane has
connected to the cluster. `AVAILABLE` alone does not mean it can; if `CLUSTERCONFIG` stays
`False`, `kubectl describe cclinfraplane edge-1 -n team-a` gives the reason, such as
`ClusterUnreachable`.

### Unregister a cluster

```bash
ccluster unregister edge-1 -n team-a
```

This removes the registration and its credentials Secret from the control plane, and leaves
the namespace. The infra plane is not touched; its ServiceAccount and token stay, so
registering the cluster again reuses them.

### List and inspect

`ccluster list` and `ccluster get` read what is registered and deployed in one control-plane
namespace. `list` prints the same columns as `kubectl get`; `get` shows one object's
conditions, and for a component set each component in rollout order with Argo CD's sync
and message and the health its chart reports. `infraplanes` can be written `infraplane` or
`ip`, and `componentsets` `componentset` or `cs`. Add `-o yaml` or `-o json` for the objects
themselves.

```bash
ccluster list infraplanes -n tenant-a
ccluster list ip -A
ccluster get infraplane edge-1 -n tenant-a
ccluster list cs -n tenant-a
ccluster get cs base -n tenant-a -o yaml
```

`list -A` (`--all-namespaces`) lists across every control-plane namespace instead, with a
`NAMESPACE` column, and so needs cluster-wide list permission on the CRDs.

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

Every merge to `main` publishes both operator images tagged `<sha12>`, the first 12
characters of the commit hash, both charts as a pre-release of the next version,
`X.Y.Z-dev.<sha12>`, and the Python package as `ccluster-py==X.Y.Z.dev<N>`, where `N` is
the commit's position on `main` (`git rev-list --count <sha>` in a full clone):

```bash
helm install ccluster-cp oci://ghcr.io/nvidia/charts/ccluster-cp --version X.Y.Z-dev.<sha12> ...
pip install "ccluster-py==X.Y.Z.dev$(git rev-list --count <sha>)"
```

The chart sets `CCLUSTER_CATALOG_REVISION` to the same `<sha12>`.

`pip` ignores these pre-releases unless pinned exactly or given `--pre`. The CLI binaries
are built only for releases. [Build and release contracts](build-and-release.md#artifact-versions-and-publishing)
has the details.

## Uninstalling

While the operators still run, delete your component sets, which uninstalls their charts
from the infra planes in reverse order, then unregister the clusters. Then `helm uninstall`
each chart. The charts keep their CRDs; deleting a CRD deletes every remaining resource of
its kind.

Uninstalling a component set's charts leaves the namespaces Argo CD created for them on
the infra plane, and the CRDs those charts installed. Delete them with `kubectl` once
nothing uses them.
