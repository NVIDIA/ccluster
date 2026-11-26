### Added
- `inference_deployment` and `dynamo_deployment` take `gatewayImplementation`, the Gateway
  API implementation behind the shared Gateway. It selects the implementation's
  templates in the new library chart `catalogs/deployments/edge`: the Service's
  `appProtocol` in both charts, and `inference_deployment`'s DestinationRule and
  scale-from-zero query. `istio` is the default and the only value supported; any other
  fails `Validate` and the render. `inference_deployment` now renders its
  DestinationRule only when `sessionAffinity` is set; without it, the DestinationRule
  had no traffic policy and changed no routing.
- **Breaking:** `inference_deployment` and `dynamo_deployment` depend on
  `catalogs/deployments/edge` through `file://../edge`. Argo CD builds the dependency
  itself; rendering a chart with Helm directly needs `helm dependency build` first.

### Changed
- **Breaking:** `inference_deployment` and `dynamo_deployment` serve through the cluster's
  shared Gateway `gateway-system/ccl-gateway` instead of an nginx Ingress. Each renders a
  Certificate `<name>-tls` from the `acme-ca` ClusterIssuer, a ListenerSet `<name>` that
  adds an HTTPS listener for its host to the Gateway, and an HTTPRoute `<name>` on that
  listener to its Service. The default host is `<name>.gateway.<domain>` instead of
  `<name>.nginx.<domain>`; `deploymentUrl` still overrides it. The infra plane needs the
  Gateway API CRDs, the shared Gateway, and cert-manager with Gateway API support.
- **Breaking:** The certificate counts toward the component's health. The charts render
  the Certificate themselves, where cert-manager used to create it for the Ingress, so an
  `inference_deployment` or `dynamo_deployment` is `Progressing` until its certificate is
  issued and `Degraded` while an issuance has failed. Without DNS for the host, its set
  does not reach `RolloutComplete`.
- **Breaking:** In Go, the `inference_deployment` and `dynamo_deployment` `Values` embed
  `deployments.Endpoint`, which holds `Domain`, `DeploymentURL`,
  `EndpointCertificateAuthority`, `EndpointBearerToken` and `GatewayImplementation`. A
  struct literal sets them through `Endpoint`; the values document does not change.
- **Breaking:** Without the nginx Ingress, the edge no longer rejects request bodies over
  15 MiB, no longer closes a connection after 900 seconds without upstream data (Istio
  sets no route timeout), and sends no `Strict-Transport-Security` header.
- **Breaking:** `endpointBearerToken` is stored in a Secret `<name>-bearer-token`,
  labelled `ccluster.nvidia.com/bearer-token: "true"` and annotated
  `ccluster.nvidia.com/host` with the deployment's host, for the Gateway's authorization
  service to enforce. No other object carries the token.
- **Breaking:** The Service the HTTPRoute targets declares `backendProtocol` as its
  `appProtocol`, `http` or `grpc`; Istio cannot route gRPC without it.
  `inference_deployment` drops its `<name>-istio-friendly` ExternalName Service.
  `dynamo_deployment` replaces it with a ClusterIP Service `<name>` on the Frontend pods,
  since the Dynamo operator's `<name>-frontend` Service declares its port as HTTP.
- **Breaking:** `inference_deployment`'s scale-from-zero trigger reads
  `istio_requests_total` that the Gateway reports for the Service, keyed on
  `destination_service_name` and `destination_service_namespace`, instead of
  `nginx_ingress_controller_requests`. Requests the Gateway's authorization service
  denies (`response_flags` `UAEX`) do not count, so a client without the bearer token can
  neither wake a deployment scaled to zero nor keep it running.
- **Breaking:** `final_stack`'s `acme-ca` ClusterIssuer solves HTTP-01 through the shared
  Gateway's `http` listener instead of the nginx ingress class. That solver needs
  cert-manager's Gateway API support, which `cluster_base.Components` now turns on; a
  plane that installs `cert_manager` another way must set
  `cert-manager.config.gatewayAPI.enabled` itself.

### Removed
- **Breaking:** Client-certificate authentication. `inference_deployment` and
  `dynamo_deployment` no longer make the edge verify client certificates or render the
  `<name>-ca-secret` Secret. A non-empty `endpointCertificateAuthority` fails `Validate`
  and makes the chart fail to render, so a deployment that set one is not served without
  the check; use `endpointBearerToken` instead.
