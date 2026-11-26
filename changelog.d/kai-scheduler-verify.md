### Added
- The ip operator verifies `kai_scheduler`: `Verified` stays False until the KAI Config
  `kai-config` exists and names the scheduler `kai-scheduler`, the `schedulerName` the
  deployments charts default to; an unset name is KAI's default, `kai-scheduler`. The
  Config and every SchedulingShard must also report `Available` for their current
  generation, as the KAI operator sets it once the components it deploys, the
  schedulers among them, are available, and at least one SchedulingShard must exist.
  The ip operator's ClusterRole now reads KAI Configs and SchedulingShards.
