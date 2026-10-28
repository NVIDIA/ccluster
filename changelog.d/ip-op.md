### Removed
- **Breaking:** the ip operator no longer verifies spire, spire_crds, external_dns,
  prometheus_adapter, fluent_bit, network_operator, opentelemetry_collector or longhorn.
  `cluster_base` still installs them.
- **Breaking:** `final_stack` drops its four `ClusterSPIFFEID`s and the SPIRE OIDC
  discovery Ingress; it now holds only the `letsencrypt-prod` ClusterIssuer.
