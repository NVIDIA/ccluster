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

The operator writes a DNSEndpoint for each registered cluster. It asks for
`*.<lb>.<cluster domain>` to point at each of the cluster's LoadBalancer Services labelled
`ccluster.nvidia.com/loadbalancer: <lb>`, such as `*.nginx.<cluster domain>` at its ingress.
The chart's bundled [external-dns](https://github.com/kubernetes-sigs/external-dns)
publishes these records. It is disabled by default, and without it nothing is published.

Create a DNS zone for the base domain at your DNS provider and delegate it from its parent
zone. Then put the provider, the zone and the credentials in a values file:

```yaml
# dns-values.yaml
external-dns:
  enabled: true
  provider:
    name: <provider>        # such as aws, azure, google or cloudflare
  domainFilters:
    - planes.example.com    # the base domain
  # Credentials, in the form the provider's tutorial gives: for example a ServiceAccount
  # annotation for workload identity, or environment variables from a Secret.
  serviceAccount:
    annotations: {}
  env: []
```

external-dns's [tutorials](https://github.com/kubernetes-sigs/external-dns/tree/master/docs/tutorials)
give each provider's name and credentials, and its chart's
[Providers](https://github.com/kubernetes-sigs/external-dns/blob/master/charts/external-dns/README.md#providers)
section maps them to chart values. Put those values under `external-dns:`, and always set
`provider.name`, which defaults to `aws`. external-dns runs as the ServiceAccount
`ccluster-cp-external-dns` in the release namespace; a provider that trusts a ServiceAccount
by name must trust this one. [Development: DNS and external-dns](development.md#dns-and-external-dns)
has a Route53 example with IRSA and its IAM policy. The chart already sets `sources`,
`namespaced` and `txtOwnerId`, so that external-dns publishes only the operator's
DNSEndpoints and deletes only records it created.

**New install.** Add the values file to the `helm install` above:

```bash
helm install ccluster-cp oci://ghcr.io/nvidia/charts/ccluster-cp --version X.Y.Z \
  -n ccluster-cp-system --create-namespace \
  --set manager.envOverrides.CCLUSTER_BASE_DOMAIN=planes.example.com \
  -f dns-values.yaml
```

**Installed release.** Helm installs a chart's CRDs only with the release, and not those of
a disabled dependency, so the release has no DNSEndpoint CRD yet. Apply it from the chart,
upgrade, then restart the operator:

```bash
CHART_DIR=$(mktemp -d)
helm pull oci://ghcr.io/nvidia/charts/ccluster-cp --version X.Y.Z --untar --untardir "$CHART_DIR"
kubectl apply -f "$CHART_DIR/ccluster-cp/charts/external-dns/crds/"
helm upgrade ccluster-cp oci://ghcr.io/nvidia/charts/ccluster-cp --version X.Y.Z \
  -n ccluster-cp-system --reuse-values -f dns-values.yaml
kubectl -n ccluster-cp-system rollout restart deploy/ccluster-cp-controller-manager
```

This upgrade only enables external-dns on the installed version: `X.Y.Z` is the version
`helm list -n ccluster-cp-system` shows. Do not change the version in the same step: with
`--reuse-values` a newer chart would keep the old catalog revision. Upgrade the version
separately, as [Control plane](#control-plane) describes. The operator
watches DNSEndpoints only if their CRD existed when it started. Without the restart it
misses external-dns reading the DNSEndpoint, and `DNSReady` stays `Pending` until the next
resync, after `CCLUSTER_INFRAPLANE_RESYNC_PERIOD` (default 5m), or for good if that is 0.

**Check.** The `DNS` column turns `True` once external-dns has read a cluster's DNSEndpoint:

```bash
kubectl get cclinfraplane -A
```

Until then, `kubectl describe cclinfraplane <name> -n <namespace>` gives the `DNSReady`
reason: `LoadBalancerPending` until the cluster has a labelled LoadBalancer with an
address, `DNSEndpointCRDMissing` without the CRD, and `Pending` until external-dns reads the
DNSEndpoint.

`True` (`Published`) does not mean the records were written. external-dns marks a
DNSEndpoint read before it calls the provider, and reports a rejected or skipped record
only in its logs ([#174](https://github.com/NVIDIA/ccluster/issues/174)). Resolve a name
under the record, and read the logs if it does not resolve:

```bash
dig +short test.nginx.edge-1.team-a.planes.example.com
kubectl -n ccluster-cp-system logs deploy/ccluster-cp-external-dns
```

Provider errors, such as denied credentials, show at the default log level. A record
outside `domainFilters`, or outside every zone the credentials can see, is dropped without
an error and shows only with `external-dns.logLevel=debug`, as
`ignoring record <name> that does not match domain filter` or a provider's message such as
Route53's `Skipping record`. One external-dns writes every cluster's records with one set of
credentials. Separate credentials per tenant need an external-dns per tenant, each reading
only its tenant's DNSEndpoints, which the chart does not set up.

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
