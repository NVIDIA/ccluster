### Added
- `catalogs/cluster_base/kai_scheduler` installs the KAI scheduler (upstream chart
  v0.15.3), which every `deployments` chart names as its `schedulerName` by default and
  whose `Queue` CRD `kai_gpu_queues` needs. Install it before both. It bin-packs GPUs,
  sets requests and limits for the scheduler, binder and operator, creates an unlimited
  `default-queue` for pods without a `kai.scheduler/queue` label, and leaves the
  admission webhook's GPU RuntimeClass check off, since the catalog's gpu-operator
  creates no `nvidia` RuntimeClass. `cluster_base.Components()` installs it after
  gpu-operator.
