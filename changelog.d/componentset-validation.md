### Added
- The cp operator validates every `CCLComponentSet` before deploying it. `Validated` now
  checks that every chart is in the catalog, that every component's values pass that chart's
  `Values` type and `Validate()`, that every component names the same CCLInfraPlane and
  that it exists and is `Available`, and that no generated Application name belongs to
  another `CCLComponentSet`. The value checks come from a
  validator registry the operator owns (`internal/controller/cp/validators.go`), which
  covers every catalog chart. A `CCLComponentSet` that fails is not deployed, and its
  message lists every failure.

### Changed
- **Breaking:** a `CCLComponentSet` must deploy every component to the same
  CCLInfraPlane; one spanning several is reported as `Validated=False/MultipleInfraPlanes`
  and not deployed. `Validated` reasons are now `SpecValid`, `UnknownChart`,
  `InvalidValues`, `MultipleInfraPlanes`, `InfraPlaneNotFound`, `InfraPlaneNotReady` and
  `NameConflict`, with `SpecValid` replacing `ComponentsAccepted`. An Application name
  owned by another set is now reported as `Validated=False/NameConflict` before any write.
- When `Validated` is `False`, `ComponentsApplied` and `Progressing` are set to
  `False/NotValidated` for the current generation, so they are no longer left over from an
  earlier spec.

### Deprecated
- `ComponentsApplied` and `Progressing` are deprecated. They will be replaced by status
  fields.
