### Added
- `catalogs/cluster_base/cert_manager` takes `cert-manager.config.gatewayAPI.enabled`
  (`CertManager.Config.GatewayAPI`), which turns on cert-manager's Gateway API support, needed
  for ACME HTTP-01 through a Gateway. It is off by default. Turn it on only where the Gateway
  API CRDs are installed: without them the cert-manager controller exits at startup.

### Changed
- **Breaking:** `catalogs/cluster_base/cert_manager` upgrades cert-manager from v1.14.5 to
  v1.21.3, which supports Kubernetes 1.33 to 1.36, and installs its CRDs with `crds.enabled`
  instead of the deprecated `installCRDs`. An existing install moves seven minor versions in
  one sync, although upstream recommends upgrading one minor version at a time. Upstream
  changes in between include
  `Certificate.spec.privateKey.rotationPolicy` defaulting to `Always` (v1.18), and HTTP-01
  solver Ingresses using `pathType: Exact` (v1.18), which ingress-nginx rejects when its
  `strict-validate-path-type` option is on; the `ACMEHTTP01IngressPathTypeExact` feature gate
  turns that off.
- **Breaking:** deleting `catalogs/cluster_base/cert_manager` leaves its CRDs (`crds.keep`),
  so Certificates and Issuers survive it.
- **Breaking:** cert-manager's chart rejects unknown values, so a misspelled key under
  `cert-manager` fails the Argo CD render, although ccluster's value validation accepts it.
