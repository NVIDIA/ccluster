### Changed
- Deleting a `CCLInfraPlane` now waits until no `CCLComponentSet` in its namespace
  references it, Terminating sets included, before removing its DNS records, its
  `CCLClusterConfig`, the mirrored Argo CD cluster Secret and its AppProject. A plane and
  its sets can therefore be deleted together, in any order: the sets tear down through the
  plane first, instead of hanging Terminating with their components left running on the
  infra-plane cluster. While it waits, the plane stays `Available` and reports
  `Deleting=True` with reason `ComponentSetsRemaining`, naming the sets, also shown in a
  new `Deleting` printer column. The plane never deletes the sets itself, so deleting only
  some of them leaves the plane, its Argo CD registration and its DNS records in place
  until the rest are deleted too.
- A `CCLComponentSet` that references a `CCLInfraPlane` being deleted reports
  `Validated=False` with the new reason `InfraPlaneDeleting` and releases nothing more
  onto it. Its teardown is unaffected.
