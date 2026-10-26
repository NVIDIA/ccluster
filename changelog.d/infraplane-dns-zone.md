### Added
- With the new `--dns-parent-zone-id`, the cp operator creates a Route53 hosted zone for
  each `CCLInfraPlane`'s domain, which must lie under the parent zone's, delegates it from
  the parent, and points `*.<domain>` in it at the plane's `ingress` LoadBalancer: an A
  record for an IP, a CNAME for a hostname, withdrawn while the plane reports no address.
  Deleting the plane deletes the delegation, the records and the zone. A new `DNSReady`
  condition, `DNS` column and `status.dns` (the zone's ID and name servers) report on it.
  Without the flag DNS stays off and reports `DNSReady=False/NotConfigured`.
  `make deploy-cp DNS_PARENT_ZONE_ID=<zone>` sets the flag.
