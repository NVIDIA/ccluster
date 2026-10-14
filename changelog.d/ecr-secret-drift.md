### Fixed
- The `ccluster-cp` chart's Argo CD leaves the `data` of Secrets labelled
  `ecr-anywhere.centml.ai/managed: "true"` out of the diff. On clusters that run
  ecr-anywhere, its webhook rewrites the `namespace_base` `ecr-image-pull` Secret on every
  write, so the namespace base stayed OutOfSync and its CCLComponentSet never completed.
