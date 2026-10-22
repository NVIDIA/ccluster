### Changed
- The cp operator requeues a `CCLComponentSet` whose status write conflicts with a newer
  version, instead of returning the conflict as a reconcile error. These conflicts are
  routine, from a lagging cache or another writer, and no longer log `Reconciler error`
  with a stack trace or count toward `controller_runtime_reconcile_errors_total`.
