### Added
- `catalogs/cluster_base/gateway_api` installs the Gateway API v1.6.1 standard-channel CRDs and
  Gateway API's safe-upgrades ValidatingAdmissionPolicy, through Envoy Gateway's CRD-only
  `gateway-crds-helm` chart v1.10.0. v1.6.1 is the version istiod 1.32 is built against. It
  takes no values. `cluster_base.Components()` installs it before `istio`, so istiod serves
  Gateway API from the start. Where it is installed, it is the only installer of these CRDs:
  another chart that bundles them, such as Envoy Gateway's `gateway-helm`, must be installed
  with its CRDs turned off, and a cluster whose CRDs come from elsewhere leaves the component
  out. Removing the component deletes the CRDs and every Gateway API object in the cluster.
