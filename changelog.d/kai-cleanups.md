### Removed
- **Breaking:** `cluster_base` no longer installs external-dns on infra planes. The
  `external_dns` component, its Go and Python values packages, and final_stack's
  `external-dns` `ClusterSPIFFEID` are removed. Ingress hosts already resolve through the
  wildcard records the control plane publishes for each plane. An existing plane keeps
  its external-dns until its cluster_base CCLComponentSet is reapplied without it; the
  cp operator then prunes the Application and Argo CD uninstalls the chart. Records it
  wrote stay in the zone until deleted by hand.
- **Breaking:** `compute_deployment` drops `domain` and `deploymentUrl`, and its SSH
  Service loses its `external-dns.alpha.kubernetes.io/hostname` annotation. A compute
  deployment is reached by its LoadBalancer address until #221 gives it a DNS name.
