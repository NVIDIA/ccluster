### Fixed
- The API server rejects a `CCLComponentSet` whose Argo CD Application name,
  `<CCLComponentSet name>-<component name>`, is longer than 53 characters. Argo CD uses
  that name as the Helm release name, which Helm caps at 53, so such a component used to
  be accepted and then never sync. The error gives the maximum component name length for
  the `CCLComponentSet`'s name, or, for a name of 52 characters or more, says the name must
  be at most 51. A `CCLComponentSet` created before this rule keeps its components, can
  still be updated and deleted, and is refused only a new component that is too long.
