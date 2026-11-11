# Quickstarts

Each quickstart does the same two things in a different way: **registers** an infra-plane
cluster with the control plane, as `edge-1` in namespace `team-a`, and **deploys** a basic
inference service onto it. The service is Hugging Face's
[text-embeddings-inference](https://github.com/huggingface/text-embeddings-inference)
serving `BAAI/bge-small-en-v1.5` on CPU, so no GPU is needed. It is deployed as two
component sets, each rolled out in order:

1. `platform`: the platform components the service needs, installed by the control plane
   (auto-mode): Prometheus, Istio, ingress-nginx, KEDA and Argo Rollouts.
2. `embeddings`: the service itself.

| Directory | Uses | Register | Deploy |
| --- | --- | --- | --- |
| [`python/`](python/) | The Python SDK, `pip install ccluster-py` | `register.py` | `deploy.py` |
| [`go/`](go/) | The Go SDK, [`pkg/cclclient`](../../pkg/cclclient/) | `register/` | `deploy/` |
| [`helm/`](helm/) | Helm charts that template the resources | `infraplane/` | `componentset/` with `platform-values.yaml`, `embeddings-values.yaml` |
| [`kubectl/`](kubectl/) | Plain manifests for a single cluster that registers itself, used by the [development setup](../../README.md#developing-ccluster) | `v1alpha1_cclinfraplane.yaml` | `v1alpha1_cclcomponentset.yaml` |

## Before you start

- The CCluster operators are installed: the control-plane chart in one cluster and the
  infra-plane chart in the cluster to register; see the [README](../../README.md#install).
- The infra-plane cluster supports LoadBalancer Services, as managed Kubernetes on any
  cloud does. The service is reached through the ingress controller's load balancer.
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

The ServiceAccount is cluster-admin, because Argo CD installs CRDs and other cluster-scoped
objects there, and whoever can read the token has those rights. `SERVER` must be an address
the control-plane cluster can reach.

`ccluster register edge-1 -n team-a --infra-plane "$IP"` does this step and the next in one
command.

## 2. Register the cluster

**Python:**

```bash
python python/register.py --context "$CP" --namespace team-a \
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

## 3. Deploy the inference service

**Python** and **Go** deploy both sets, wait for each to roll out, and print a request to
try:

```bash
python python/deploy.py --context "$CP" --namespace team-a --infra-plane edge-1
# or, from the repository root:
go run ./examples/quickstart/go/deploy -context "$CP" -namespace team-a -infra-plane edge-1
```

**Helm:** install the platform set, wait until its `PROGRESS` is `RolloutComplete`, then install the
service with the cluster's domain:

```bash
helm --kube-context "$CP" install platform helm/componentset -n team-a \
  -f helm/platform-values.yaml --set infraPlane=edge-1
kubectl --context "$CP" -n team-a get cclcomponentset platform -w   # until PROGRESS is RolloutComplete

DOMAIN=$(kubectl --context "$CP" -n team-a get cclinfraplane edge-1 -o jsonpath='{.status.domain}')
helm --kube-context "$CP" install embeddings helm/componentset -n team-a \
  -f helm/embeddings-values.yaml --set infraPlane=edge-1 --set domain="$DOMAIN"
kubectl --context "$CP" -n team-a get cclcomponentset embeddings -w
```

## 4. Call the service

The service is served at `https://embeddings.nginx.<domain>`. Until DNS is published for
the cluster (see [Installation: DNS](../../docs/installation.md#dns)), resolve the name to
the ingress's address yourself; `-k` accepts the ingress's default certificate:

```bash
INGRESS=$(kubectl --context "$IP" -n ingress-nginx get svc \
  -l ccluster.nvidia.com/loadbalancer=nginx -o jsonpath='{.items[0].status.loadBalancer.ingress[0].ip}')
curl -k "https://embeddings.nginx.$DOMAIN/embed" --resolve "embeddings.nginx.$DOMAIN:443:$INGRESS" \
  -H 'Content-Type: application/json' -d '{"inputs": "Hello, CCluster"}'
```

On clouds whose load balancers have a hostname rather than an IP, such as AWS, read
`.hostname` instead and resolve it to an address first.

## Clean up

Remove the service before the platform components it depends on. Removing a set
uninstalls its charts from the cluster, in reverse order; wait for each to disappear.

```bash
python python/deploy.py --context "$CP" --namespace team-a --delete
python python/register.py --context "$CP" --namespace team-a edge-1 --delete
# or with Go: add -delete to the same commands
# or with Helm:
helm --kube-context "$CP" uninstall embeddings -n team-a   # wait until the set is gone
helm --kube-context "$CP" uninstall platform -n team-a     # wait until the set is gone
helm --kube-context "$CP" uninstall edge-1 -n team-a
```

Unregistering leaves the infra plane untouched;
`kubectl --context "$IP" delete -f infra-plane-access.yaml` removes the ServiceAccount.
Delete `token` and `ca.crt`.
