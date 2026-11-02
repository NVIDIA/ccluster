### Changed
- The operators, the API package and the generated Go client in
  `github.com/NVIDIA/ccluster/pkg/client` are built against Kubernetes v0.37.1 libraries
  and controller-runtime v0.25.2. The client's shared informer factory gains
  `StartWithContext`, `WaitForCacheSyncWithContext` and per-informer options.
- **Breaking:** `SchemeBuilder` in `api/ccluster/v1alpha1` is now an apimachinery
  `runtime.SchemeBuilder` rather than controller-runtime's deprecated `scheme.Builder`,
  so the API package no longer imports controller-runtime. `AddToScheme` is unchanged.
