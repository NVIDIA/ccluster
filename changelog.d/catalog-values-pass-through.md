### Changed
- `ccluster-py` catalog `Values` models keep keys they do not model, at any depth, and
  `catalogs.entry` and `catalogs.component` pass them to the chart unchanged, as the
  operator already does. A chart value outside the Go type, such as
  `ingress-nginx.controller.podLabels`, no longer needs a separate side channel; a
  misspelled key is no longer rejected. The generated schemas no longer set
  `additionalProperties: false`. The CRD models are unchanged.
