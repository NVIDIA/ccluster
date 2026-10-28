### Added
- The ip operator verifies metrics_server: the `v1beta1.metrics.k8s.io` APIService must
  be Available. The check holds whichever metrics-server serves it, including a
  provider's own on GKE, AKS, OpenShift or k3s.
