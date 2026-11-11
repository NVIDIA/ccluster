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
- The control plane publishes DNS for its clusters; see
  [Installation: DNS](../../docs/installation.md#dns).
- The infra-plane cluster supports LoadBalancer Services, as managed Kubernetes on any
  cloud does, and its ingress load balancer is reachable from the internet on ports 80 and
  443, so that Let's Encrypt can issue the model server's certificate.
- You have kubeconfig contexts for both clusters. Below, `$CP` and `$IP` stand for them.

The commands below run from this directory.

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
cluster-scoped objects there, and whoever can read the token has those rights. `SERVER` must be an address
the control-plane cluster can reach.

`ccluster register edge-1 -n team-a --infra-plane "$IP"` does this step and the next in one
command.

## 2. Register the cluster

**Python:**

```bash
python ../../python/examples/register.py --context "$CP" --namespace team-a \
  edge-1 "$SERVER" --token-file token --ca-file ca.crt
```

**Go**, from the repository root:

```bash
go run ./examples/quickstart/go/register -context "$CP" -namespace team-a \
  -name edge-1 -server "$SERVER" -token-file examples/quickstart/token \
  -ca-file examples/quickstart/ca.crt
```

**Helm:** the release name is the name the cluster is registered as.

```bash
helm --kube-context "$CP" install edge-1 helm/infraplane -n team-a --create-namespace \
  --set server="$SERVER" --set-file bearerToken=token --set-file caCert=ca.crt
kubectl --context "$CP" -n team-a get cclinfraplane edge-1   # wait for AVAILABLE True
```

The cluster's domain is `edge-1.team-a.<base domain>`; `kubectl get cclinfraplane` shows it.

## 3. Deploy the model

**Python** and **Go** deploy both sets, wait for each to roll out, and print a request to
try:

```bash
python ../../python/examples/deploy.py --context "$CP" --namespace team-a --infra-plane edge-1
# or, from the repository root:
go run ./examples/quickstart/go/deploy -context "$CP" -namespace team-a -infra-plane edge-1
```

**Helm:** install the dependencies, wait until their `PROGRESS` is `RolloutComplete`, then
install the model server with the cluster's domain:

```bash
helm --kube-context "$CP" install dependencies helm/componentset -n team-a \
  -f helm/dependencies-values.yaml --set infraPlane=edge-1
kubectl --context "$CP" -n team-a get cclcomponentset dependencies -w

DOMAIN=$(kubectl --context "$CP" -n team-a get cclinfraplane edge-1 -o jsonpath='{.status.domain}')
helm --kube-context "$CP" install qwen helm/componentset -n team-a \
  -f helm/qwen-values.yaml --set infraPlane=edge-1 --set domain="$DOMAIN"
kubectl --context "$CP" -n team-a get cclcomponentset qwen -w
```

## 4. Ask it something

The model is served at `https://qwen.nginx.<domain>`, with an OpenAI-compatible API:

```bash
curl "https://qwen.nginx.$DOMAIN/v1/chat/completions" -H 'Content-Type: application/json' \
  -d '{"messages": [{"role": "user", "content": "What is Kubernetes, in one sentence?"}]}'
```

## Clean up

Remove the model server before the dependencies it needs. Removing a set
uninstalls its charts from the cluster, in reverse order; wait for each to disappear.

```bash
python ../../python/examples/deploy.py --context "$CP" --namespace team-a --delete
python ../../python/examples/register.py --context "$CP" --namespace team-a edge-1 --delete
# or with Go: add -delete to the same commands
# or with Helm:
helm --kube-context "$CP" uninstall qwen -n team-a           # wait until the set is gone
helm --kube-context "$CP" uninstall dependencies -n team-a   # wait until the set is gone
helm --kube-context "$CP" uninstall edge-1 -n team-a
```

Unregistering leaves the infra plane untouched;
`kubectl --context "$IP" delete -f infra-plane-access.yaml` removes the ServiceAccount.
Delete `token` and `ca.crt`.
