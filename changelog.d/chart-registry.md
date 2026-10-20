### Changed
- The CRD readiness helper that `spirecrds.Verify` uses moved from `catalogs.CRDsEstablished`
  to `common.CRDsEstablished` in the new `catalogs/common` package, which holds helpers
  shared by catalog chart packages.
