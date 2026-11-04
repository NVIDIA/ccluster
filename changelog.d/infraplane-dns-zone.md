### Added
- The cp operator writes an external-dns `DNSEndpoint` per `CCLInfraPlane` into its own
  namespace (or `--dns-endpoint-namespace`), pointing `*.<name>.<domain>` at each of the
  plane's LoadBalancers labelled `ccluster.nvidia.com/loadbalancer: <name>`, such as
  `*.ingress.<domain>` at its ingress: an A or AAAA record for an IPv4 or IPv6 address, in
  canonical form and with an IPv4-mapped IPv6 address as IPv4, a CNAME for a hostname. A
  LoadBalancer without an address has no record, and the DNSEndpoint is withdrawn while
  none has one and when the plane is deleted. The records go into whichever DNS zone the
  domain falls under, which the user creates and delegates: one per plane, or one shared.
  A new `DNSReady` condition and `DNS` column report on it, `DNSEndpointCRDMissing` while
  external-dns is not installed, `InvalidLoadBalancer` for a LoadBalancer that gets no
  record because its label value cannot be a DNS label or its address is not valid, such
  as an IP with a zone or a hostname that is not a lowercase DNS name, and
  `DNSEndpointError` when the DNSEndpoint cannot be written. The operator's ClusterRole
  gains `externaldns.k8s.io` `dnsendpoints`.
- The cp chart gains an optional external-dns dependency (`external-dns.enabled`, off by
  default) that publishes those DNSEndpoints and nothing else: it reads only the release
  namespace and only the `crd` source. Its provider, zones and credentials are set through
  its values. `make deploy-cp DNS=true` enables it.

### Changed
- **Breaking:** The `final_stack` SPIRE OIDC discovery Ingress moves from
  `oidc-discovery.<domain>` to `oidc-discovery.ingress.<domain>`, under the ingress's
  wildcard record. The old URL stops resolving to it, so anything configured with it,
  such as an AWS IAM OIDC provider for SPIRE JWTs, must be updated to the new URL.
- **Breaking:** The `inference_deployment` and `dynamo_deployment` Ingress host moves from
  `<name>.<namespace>.<domain>` to `<name>.ingress.<domain>`, under the ingress's wildcard
  record; `domain` is now the plane's domain. The namespace is no longer part of the host,
  so two deployments with the same name on one plane claim the same host.
