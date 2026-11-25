### Fixed
- A `CCLComponentSet` whose validation fails, such as one referencing a `CCLInfraPlane`
  that is not `Available`, now still deletes the Applications of components removed from
  its spec once every remaining component has finished, and reports `ComponentRemoving`
  meanwhile. Before, removed components stayed installed until validation passed. A
  remaining component whose new spec cannot be written until validation passes, such as
  the new one in a rename, still holds removal up.
