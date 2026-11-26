### Added
- `catalogs/cluster_base/gateway_istio` creates the cluster's shared Gateway API Gateway,
  `gateway-system/ccl-gateway`, which istiod serves. It has one HTTP listener on port 80, for
  ACME HTTP-01 challenges. Deployments attach their own HTTPS listeners through ListenerSets,
  and routes, from namespaces labelled `ccluster.nvidia.com/tenant: "true"`. Its Service is
  labelled as LoadBalancer `gateway`, so the control plane publishes `*.gateway.<domain>`.
  istiod scales it from 4 to 180 replicas with a PodDisruptionBudget of 50%, the settings
  `ingress_nginx` uses. `cluster_base.Components()` installs it after `istio`.

### Changed
- `catalogs/namespace_base` labels the tenant namespace `ccluster.nvidia.com/tenant: "true"`
  and admits traffic from `gateway-system` in its NetworkPolicy and AuthorizationPolicy, so
  the shared Gateway can attach the namespace's listeners and routes and reach its pods. Its
  NetworkPolicy also lets the namespace's pods reach the Gateway's pods, which a call to a
  deployment's public host in the same cluster reaches when the LoadBalancer reports an IP.
