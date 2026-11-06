### Added
- `catalogs/cluster_base/kai_scheduler` installs the KAI scheduler (upstream chart
  v0.15.3), which every `deployments` chart names as its `schedulerName` by default and
  whose `Queue` CRD `kai_gpu_queues` needs. Install it before both. It bin-packs GPUs,
  sets requests and limits for the scheduler, binder and operator, and creates an
  unlimited `default-queue` for pods without a `kai.scheduler/queue` label. The admission
  webhook does not set the `nvidia` RuntimeClass on GPU pods, because the catalog runs
  gpu-operator with its container toolkit off, and a node's containerd may then have no
  `nvidia` runtime handler. A checksum ConfigMap resyncs the Application when values
  that reach the hook-applied `kai-config` Config CR change. `cluster_base.Components()`
  installs it after gpu-operator.
