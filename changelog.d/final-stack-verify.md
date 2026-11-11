### Added
- The ip operator verifies `final_stack`: `Verified` stays False until the cert-manager
  ClusterIssuer `acme-ca`, which the inference and dynamo Ingresses request, reports
  Ready. For the chart's ACME issuer that means cert-manager's controller runs and has
  registered the account with Let's Encrypt, so a cluster that cannot reach Let's Encrypt
  fails. An `acme-ca` issuer of another type passes too. The ip operator's ClusterRole
  now reads ClusterIssuers.
