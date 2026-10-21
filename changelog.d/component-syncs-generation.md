### Fixed
- `CCLComponentSet` rebuilds `status.componentSyncs` and the `Progressing` condition on a
  pass that fails validation or fails to apply. Before, such a pass advanced
  `status.observedGeneration` but kept the previous spec's report, so
  `comparedToMatchesSpec` could read `true`, and `Progressing` could read
  `RolloutComplete`, for a spec Argo CD had not compared.
- `CCLComponentSet` status leaves out an Application whose back-reference names another
  `CCLComponentSet`. Before, `status.componentSyncs` could report that Application's health
  and sync as one of its own components.
