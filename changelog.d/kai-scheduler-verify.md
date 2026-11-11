### Added
- The ip operator verifies `kai_scheduler`: `Verified` stays False until the KAI Config
  `kai-config` exists and names the scheduler `kai-scheduler`, the `schedulerName` the
  deployments charts default to; an unset name is KAI's default, `kai-scheduler`. It
  checks KAI's configuration, not that the scheduler runs. The ip operator's ClusterRole
  now reads KAI Configs.
