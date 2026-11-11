### Added
- `catalogs/cluster_base/dynamo_platform` installs the NVIDIA Dynamo platform (upstream
  chart 1.5.1): the Dynamo operator that serves `dynamo_deployment`'s
  `DynamoGraphDeployment`, and its NATS event plane, configured as the legacy platform's
  production clusters run it. NATS keeps a 10Gi JetStream volume on the default
  StorageClass. Grove (v0.1.0-alpha.13, the chart's bundled subchart) is off; when it is
  turned on, Argo CD owns the `grove.io` CRDs, so Grove's CRD installer stays off, and
  Grove's operator creates its own webhook certificate Secret.
  `cluster_base.Components()` installs it last.
