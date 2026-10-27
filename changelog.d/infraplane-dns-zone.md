### Added
- The cp operator writes an external-dns `DNSEndpoint` per `CCLInfraPlane` into its own
  namespace (or `--dns-endpoint-namespace`), pointing `*.<domain>` at the plane's `ingress` LoadBalancer: an A record for
  an IP, a CNAME for a hostname, withdrawn while the plane reports no address and when the
  plane is deleted. The records go into whichever DNS zone the domain falls under, which
  the user creates and delegates: one per plane, or one shared. A new `DNSReady` condition
  and `DNS` column report on it, `DNSEndpointCRDMissing` while external-dns is not
  installed. The operator's ClusterRole gains `externaldns.k8s.io` `dnsendpoints`.
- The cp chart gains an optional external-dns dependency (`external-dns.enabled`, off by
  default) that publishes those DNSEndpoints and nothing else: it reads only the release
  namespace and only the `crd` source. Its provider, zones and credentials are set through
  its values. `make deploy-cp DNS=true` enables it.
