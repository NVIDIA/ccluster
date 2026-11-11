### Added
- The cp operator takes `--componentset-max-concurrent-reconciles` (default 4), the number
  of `CCLComponentSet`s it reconciles at once, for the rollout and for component health
  each. Before, it reconciled them one at a time.
  `CCLInfraPlane`s are still reconciled one at a time.

### Changed
- The cp operator skips the `CCLComponentSet` status write when the status is unchanged,
  such as after an Argo CD Application refresh that changes nothing the status reports.
  This saves one API request per such reconcile.
