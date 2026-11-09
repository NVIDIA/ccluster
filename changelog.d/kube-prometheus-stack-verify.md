### Added
- The ip operator verifies `kube_prometheus_stack`: `Verified` stays False until the
  Service `prometheus/prometheus-op-kube-prometh-prometheus`, the address the inference
  and dynamo ScaledObjects and prometheus-adapter hardcode, has a ready endpoint. A
  Prometheus under another name or namespace fails. The ip operator's ClusterRole now
  reads EndpointSlices.
