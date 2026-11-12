# Quickstarts

Each quickstart does the same two things in a different way: **registers** an infra-plane
cluster with the control plane, as `edge-1` in namespace `team-a`, and **serves** a small
LLM on it: [Qwen2.5-0.5B-Instruct](https://huggingface.co/Qwen/Qwen2.5-0.5B-Instruct),
served by [llama.cpp](https://github.com/ggml-org/llama.cpp) on CPU, so no GPU is needed.
It is deployed as two component sets, each rolled out in order:

1. `dependencies`: the infra-plane dependencies the model server needs, installed by the
   control plane (auto-mode): Prometheus, Istio, cert-manager, ingress-nginx, the Let's
   Encrypt issuer, KEDA and Argo Rollouts.
2. `qwen`: the model server.

| Directory | Uses | Register | Deploy |
| --- | --- | --- | --- |
| [`python/examples/`](../../python/examples/) | The Python SDK, `pip install ccluster-py` | `register.py` | `deploy.py` |
| [`go/`](go/) | The Go SDK, [`pkg/cclclient`](../../pkg/cclclient/) | `register/` | `deploy/` |
| [`helm/`](helm/) | Helm charts that template the resources | `infraplane/` | `componentset/` with `dependencies-values.yaml`, `qwen-values.yaml` |
| [`manifests/`](manifests/) | Plain manifests for a single cluster that registers itself, used by the [development setup](../../README.md#developing-ccluster) | `v1alpha1_cclinfraplane.yaml` | `v1alpha1_cclcomponentset.yaml` |

## Before you start

- The CCluster operators are installed: the control-plane chart in one cluster and the
  infra-plane chart in the cluster to register; see the [README](../../README.md#install).
- A clone of this repository at the release you installed, `vX.Y.Z`. The commands below
  run from its `examples/quickstart` directory.
- For the Python quickstart, Python 3.10 or newer and `pip install ccluster-py==X.Y.Z`;
  for the Go one, Go 1.21 or newer.
- The control plane publishes DNS for its clusters; see
  [Installation: DNS](../../docs/installation.md#dns).
- The infra-plane cluster supports LoadBalancer Services, as managed Kubernetes on any
  cloud does, and its ingress load balancer is reachable from the internet on ports 80 and
  443, so that Let's Encrypt can issue the model server's certificate.
- The infra-plane cluster serves the resource metrics API, which the autoscalers of
  istiod and ingress-nginx read: `kubectl --context "$IP" top nodes` prints usage. GKE and
  AKS serve it; on EKS, kubeadm or kind, install metrics-server in step 2.
- Kubeconfig contexts for both clusters. The commands below use them as `$CP` and `$IP`:

  ```bash
  CP=<control-plane context>
  IP=<infra-plane context>   # the same context when one cluster plays both roles
  ```

## 1. Give the control plane access to the cluster

The control plane reaches the infra-plane cluster with a ServiceAccount token. Create the
ServiceAccount on the infra plane and save its token, CA certificate and API server
address:

```bash
kubectl --context "$IP" apply -f infra-plane-access.yaml
kubectl --context "$IP" -n kube-system get secret ccluster-manager-token \
  -o jsonpath='{.data.token}' | base64 -d > token
kubectl --context "$IP" -n kube-system get secret ccluster-manager-token \
  -o jsonpath='{.data.ca\.crt}' | base64 -d > ca.crt
SERVER=$(kubectl config view --minify --flatten --context "$IP" \
  -o jsonpath='{.clusters[0].cluster.server}')
```

The ServiceAccount is cluster-admin, because the control plane installs CRDs and other
cluster-scoped objects there, and whoever can read the token has those rights.

`SERVER` must be an address the control-plane cluster can reach. A kind cluster's
kubeconfig points at `127.0.0.1`, which it cannot: use `SERVER=https://kubernetes.default.svc`
when the infra plane is the control-plane cluster itself, and
`SERVER=https://<kind cluster name>-control-plane:6443` for another kind cluster.

`ccluster register edge-1 -n team-a --infra-plane "$IP"` does this step and the next in one
command.

## 2. Register the cluster

**Python:**

```bash
python ../../python/examples/register.py --context "$CP" --namespace team-a \
  edge-1 "$SERVER" --token-file token --ca-file ca.crt
```

**Go:**

```bash
go run ./go/register -context "$CP" -namespace team-a \
  -name edge-1 -server "$SERVER" -token-file token -ca-file ca.crt
```

**Helm:** the release name is the name the cluster is registered as.

```bash
helm --kube-context "$CP" install edge-1 helm/infraplane -n team-a --create-namespace \
  --set server="$SERVER" --set-file bearerToken=token --set-file caCert=ca.crt
```

Then check that the control plane reached the cluster:

```bash
kubectl --context "$CP" -n team-a get cclinfraplane edge-1   # wait for CLUSTERCONFIG True
```

`CLUSTERCONFIG` turns `True` once the control plane has connected to the cluster and
written its domain there, `edge-1.team-a.<base domain>`. `AVAILABLE` alone does not mean
it can connect. If `CLUSTERCONFIG` stays `False`,
`kubectl --context "$CP" -n team-a describe cclinfraplane edge-1` gives the reason, such
as `ClusterUnreachable` for a `SERVER` the control plane cannot reach.

On EKS, kubeadm or kind, install metrics-server now, as a component set of its own, and
let it roll out. On kind, first uncomment the values in `helm/metrics-server-values.yaml`.

```bash
helm --kube-context "$CP" install metrics-server helm/componentset -n team-a \
  -f helm/metrics-server-values.yaml --set infraPlane=edge-1
kubectl --context "$CP" -n team-a get cclcomponentset metrics-server -w   # Ctrl-C at RolloutComplete
```

## 3. Deploy the model

**Python** and **Go** deploy both sets, wait for each to roll out, and print a request to
try:

```bash
python ../../python/examples/deploy.py --context "$CP" --namespace team-a --infra-plane edge-1
# or:
go run ./go/deploy -context "$CP" -namespace team-a -infra-plane edge-1
```

**Helm:** install the dependencies, wait until their `PROGRESS` is `RolloutComplete`, then
install the model server with the cluster's domain:

```bash
helm --kube-context "$CP" install dependencies helm/componentset -n team-a \
  -f helm/dependencies-values.yaml --set infraPlane=edge-1
kubectl --context "$CP" -n team-a get cclcomponentset dependencies -w   # Ctrl-C at RolloutComplete

DOMAIN=$(kubectl --context "$CP" -n team-a get cclinfraplane edge-1 -o jsonpath='{.status.domain}')
helm --kube-context "$CP" install qwen helm/componentset -n team-a \
  -f helm/qwen-values.yaml --set infraPlane=edge-1 --set domain="$DOMAIN"
kubectl --context "$CP" -n team-a get cclcomponentset qwen -w   # Ctrl-C at RolloutComplete
```

cert-manager requests the model server's certificate after the set has rolled out.
`kubectl --context "$IP" -n qwen get certificate` shows `READY True` once HTTPS works.

## 4. Ask it something

The model is served at `https://qwen.nginx.<domain>`, with an OpenAI-compatible API:

```bash
DOMAIN=$(kubectl --context "$CP" -n team-a get cclinfraplane edge-1 -o jsonpath='{.status.domain}')
curl "https://qwen.nginx.$DOMAIN/v1/chat/completions" -H 'Content-Type: application/json' \
  -d '{"messages": [{"role": "user", "content": "What is Kubernetes, in one sentence?"}]}'
```

Without published DNS, as on a local cluster, the certificate cannot be issued. Call the
model through the ingress load balancer's address instead, accepting the ingress's
default certificate:

```bash
INGRESS=$(kubectl --context "$IP" -n ingress-nginx get svc \
  -l ccluster.nvidia.com/loadbalancer=nginx -o jsonpath='{.items[0].status.loadBalancer.ingress[0].ip}')
curl -k "https://qwen.nginx.$DOMAIN/v1/chat/completions" --resolve "qwen.nginx.$DOMAIN:443:$INGRESS" \
  -H 'Content-Type: application/json' \
  -d '{"messages": [{"role": "user", "content": "What is Kubernetes, in one sentence?"}]}'
```

## When a set does not finish

`PROGRESS` names the component the set is waiting on. Each component is an Argo CD
Application named `<set>-<component>`, in the set's namespace, whose conditions say what
went wrong:

```bash
kubectl --context "$CP" -n team-a get cclcomponentset
kubectl --context "$CP" -n team-a get applications
kubectl --context "$CP" -n team-a get application dependencies-istio -o jsonpath='{.status.conditions}'
```

## Clean up

Remove the model server before the dependencies it needs, and every set before the
registration. Removing a set uninstalls its charts from the cluster, in reverse order;
wait for each to disappear.

```bash
# The model server, then the dependencies:
python ../../python/examples/deploy.py --context "$CP" --namespace team-a --delete
# or with Go:
go run ./go/deploy -context "$CP" -namespace team-a -delete
# or with Helm:
helm --kube-context "$CP" uninstall qwen -n team-a
kubectl --context "$CP" -n team-a wait --for=delete cclcomponentset/qwen --timeout=10m
helm --kube-context "$CP" uninstall dependencies -n team-a
kubectl --context "$CP" -n team-a wait --for=delete cclcomponentset/dependencies --timeout=10m

# metrics-server, if you installed it in step 2:
helm --kube-context "$CP" uninstall metrics-server -n team-a
kubectl --context "$CP" -n team-a wait --for=delete cclcomponentset/metrics-server --timeout=10m

# The registration:
python ../../python/examples/register.py --context "$CP" --namespace team-a edge-1 --delete
# or with Go:
go run ./go/register -context "$CP" -namespace team-a -name edge-1 -delete
# or with Helm:
helm --kube-context "$CP" uninstall edge-1 -n team-a
```

Unregistering leaves the infra plane untouched;
`kubectl --context "$IP" delete -f infra-plane-access.yaml --ignore-not-found` removes the
ServiceAccount. Removing the sets also leaves the namespaces Argo CD created there and the
CRDs the charts installed; delete them with `kubectl` once nothing uses them. Delete
`token` and `ca.crt`.
