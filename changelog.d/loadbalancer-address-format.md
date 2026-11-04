### Changed
- The cp operator checks each LoadBalancer address in the infra plane's
  `status.loadBalancers` before publishing its DNS record. An IP is published in
  canonical form, and an IPv4-mapped IPv6 address as IPv4. A LoadBalancer whose IP has a
  zone or whose hostname is not a lowercase DNS name gets no record and is reported in
  `DNSReady`, whose `InvalidLoadBalancerName` reason is now `InvalidLoadBalancer`.
