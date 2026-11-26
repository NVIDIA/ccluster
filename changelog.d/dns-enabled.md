### Changed
- **Breaking:** The cp operator writes a `DNSEndpoint` for each `CCLInfraPlane` only with
  `--dns-enabled` (`CCLUSTER_DNS_ENABLED`), off by default. Without it, it writes none,
  deletes those it wrote earlier, and reports `DNSReady` `False/DNSDisabled`, so an
  external-dns that reads DNSEndpoints in every namespace no longer publishes records for
  a control plane that never turned DNS on. A release with the chart's external-dns
  enabled must also set `manager.envOverrides.CCLUSTER_DNS_ENABLED=true`, as
  [Installation: DNS](docs/installation.md#dns) now does, or the upgrade deletes its
  DNSEndpoints and external-dns withdraws the records. `make deploy-cp DNS=true` sets both.
