### Added
- The ip operator verifies `keda`: `Verified` stays False until the CustomResourceDefinition
  `scaledobjects.keda.sh`, the kind inference and dynamo deployments render, exists and
  the APIService `v1beta1.external.metrics.k8s.io`, which the HPAs KEDA creates read, is
  Available. It does not check KEDA's operator.
