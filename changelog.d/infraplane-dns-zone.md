### Added
- The cp operator writes an external-dns `DNSEndpoint` per `CCLInfraPlane` into its own
  namespace (or `--dns-endpoint-namespace`), pointing `*.<name>.<domain>` at each of the
  plane's LoadBalancers labelled `ccluster.nvidia.com/loadbalancer: <name>`, such as
  `*.ingress.<domain>` at its ingress: an A or AAAA record for an IPv4 or IPv6 address, a
  CNAME for a hostname. A LoadBalancer without an address has no record, and the
  DNSEndpoint is withdrawn while none has one and when the plane is deleted. The records
  go into whichever DNS zone the domain falls under, which the user creates and
  delegates: one per plane, or one shared. A new `DNSReady` condition and `DNS` column
  report on it, `DNSEndpointCRDMissing` while external-dns is not installed,
  `InvalidLoadBalancerName` for a label value that cannot be a DNS label and
  `DNSEndpointError` when the DNSEndpoint cannot be written. The operator's
  ClusterRole gains `externaldns.k8s.io` `dnsendpoints`.
- The cp chart gains an optional external-dns dependency (`external-dns.enabled`, off by
  default) that publishes those DNSEndpoints and nothing else: it reads only the release
  namespace and only the `crd` source. Its provider, zones and credentials are set through
  its values. `make deploy-cp DNS=true` enables it.

### Changed
- The `final_stack` SPIRE OIDC discovery Ingress moves to
  `oidc-discovery.ingress.<domain>`, under the ingress's wildcard record.
