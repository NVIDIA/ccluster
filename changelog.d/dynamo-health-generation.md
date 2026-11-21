### Fixed
- `dynamo_deployment` health reports a DynamoGraphDeployment whose current spec failed to
  reconcile as `Degraded`, with the `Ready` condition's reason. A failed reconcile moves the
  `Ready` condition's `observedGeneration` but not `status.observedGeneration`, so the graph
  stayed `Progressing` with no reason. The rollout still waits on a `Degraded` graph.
