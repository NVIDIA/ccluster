### Added
- The ip operator verifies metrics_server: the `v1beta1.metrics.k8s.io` APIService must
  be Available. The check holds whichever metrics-server serves it, including a
  provider's own on GKE, AKS, OpenShift or k3s.

### Removed
- **Breaking:** the ip operator no longer verifies spire, spire_crds, external_dns,
  prometheus_adapter, fluent_bit, network_operator, opentelemetry_collector or longhorn.
  `cluster_base` still installs them.
- **Breaking:** `final_stack` drops its four `ClusterSPIFFEID`s and the SPIRE OIDC
  discovery Ingress; it now holds only the `letsencrypt-prod` ClusterIssuer.
