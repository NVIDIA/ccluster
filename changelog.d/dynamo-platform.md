### Added
- `catalogs/cluster_base/dynamo_platform` installs the NVIDIA Dynamo platform (upstream
  chart 1.5.1): the Dynamo operator that serves `dynamo_deployment`'s
  `DynamoGraphDeployment`, its NATS event plane, and Grove (v0.1.0-alpha.13, the chart's
  bundled subchart) with the operator's Grove integration on. Argo CD owns the `grove.io`
  CRDs, so Grove's CRD installer is off, and Grove's operator creates its own webhook
  certificate Secret. NATS keeps a 10Gi JetStream volume on the default StorageClass.
  `cluster_base.Components()` installs it last.
