### Removed
- **Breaking:** `cluster_base` no longer installs external-dns on infra planes. The
  `external_dns` component and its Go and Python values packages are removed. Ingress
  hosts already resolve through the wildcard records the control plane publishes for each
  plane. An existing plane keeps its external-dns until its `cluster_base` CCLComponentSet
  is reapplied without it, when the cp operator prunes the Application. Records it wrote
  stay in the zone until deleted by hand.
- **Breaking:** `cluster_base` no longer installs SPIRE. The `spire` and `spire_crds`
  components and their Go and Python values packages are removed, with final_stack's
  ClusterSPIFFEIDs and SPIRE OIDC discovery Ingress and the unused spiffe-helper
  ConfigMaps in `cert_manager`, `fluent_bit` and `kube_prometheus_stack`. `final_stack`
  now holds only the ClusterIssuer and no longer reads `global.cluster.domain`. An
  existing plane keeps SPIRE until its `cluster_base` CCLComponentSet is reapplied.
- **Breaking:** `compute_deployment` drops `domain` and `deploymentUrl`, and its SSH
  Service loses its `external-dns.alpha.kubernetes.io/hostname` annotation. A compute
  deployment is reached by its LoadBalancer address until #221 gives it a DNS name.
- **Breaking:** `spirecrds.Verify` and `catalogs/common` are removed, so every
  `cluster_base` component is verified without a check and a `CCLClusterConfig` reports
  `Verified=True` as soon as it is reconciled. The ip operator's ClusterRole no longer
  reads CustomResourceDefinitions.
