### Added
- The ip operator verifies `metrics_server`: `Verified` stays False until the
  `v1beta1.metrics.k8s.io` APIService is Available, which HorizontalPodAutoscalers and
  KEDA's cpu and memory triggers need. The check accepts any metrics server, so a managed
  cluster's built-in one passes and the chart should be left out there. The ip
  operator's ClusterRole now reads APIServices.
