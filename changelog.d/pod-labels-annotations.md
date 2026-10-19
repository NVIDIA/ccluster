### Added
- `inference_deployment`, `compute_deployment`, `job_deployment` and `dynamo_deployment`
  take `podLabels` and `podAnnotations`, added verbatim to every pod the chart creates,
  including the Dynamo Frontend and workers. A key the chart sets itself keeps the chart's
  value. They are for the caller's own metadata and must never carry tenant input, which
  belongs in `userAnnotations`. Rendered output is unchanged when they are unset.
