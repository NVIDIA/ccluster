### Added
- `status.componentSyncStatuses` keeps an entry for a component removed from the spec until
  its Application is gone, with `removal` set to `DeletePending` or `Deleting`.
  `status.componentHealthStatuses` lists it too, judged by the default health function.
  `Progressing` reports the reason `ComponentRemoving` while those Applications are deleted.

### Changed
- Updates follow list order, like the first rollout: a component's new spec is written only
  once every component before it has synced its own current spec and is `Healthy` or
  `Succeeded`. An edit that changes several components reaches them one at a time, and a
  component that does not finish holds back updates to every component after it.
- The Application of a component removed from the spec is deleted only after every
  remaining component has finished, one at a time, highest `ccluster.nvidia.com/wave` label
  first. Before, it was deleted in the same pass that wrote the new spec. `Progressing`
  stays `True` until the last one is gone.
- Deleting a `CCLComponentSet` while removals still wait deletes the removed components'
  Applications first, then the remaining components' in reverse order.
