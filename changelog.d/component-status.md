### Added
- `CCLComponentSet` reports `status.componentHealthStatuses`: each released component's
  `health` (`Healthy`, `Progressing`, `Degraded`, `Suspended`, `Missing`, `Unknown`,
  `Succeeded` or `Failed`), `reason`, `message` and `lastTransitionTime`, as its chart's
  health function judges it from Argo CD's resource tree, read through argocd-server.
  `job_deployment`, `dynamo_deployment` and `compute_deployment` judge their Job,
  DynamoGraphDeployment and load balancer; every other chart starts from Argo CD's
  aggregate health and adds its Pods, such as one in CrashLoopBackOff or waiting for a GPU.
  The reasons the operator sets itself are Go constants, `ComponentHealthReason<Reason>`,
  in the API package; a chart's own reasons are exported from its chart package, such as
  `jobdeployment.ReasonComplete`.

### Changed
- **Breaking:** `status.componentSyncs` is renamed `status.componentSyncStatuses` and loses
  `health`; health is in `status.componentHealthStatuses`.
- A component is released only once the one before it is `Healthy` or `Succeeded` as its
  chart judges it, not as Argo CD's aggregate health says. A running `job_deployment` Job
  now lets the next component follow, and a crash-looping Pod or a pending
  DynamoGraphDeployment holds it. When the resource tree cannot be read, the aggregate
  decides, as before.
