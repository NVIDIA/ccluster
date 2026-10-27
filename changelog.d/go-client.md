### Added
- A generated Go client for the API in `github.com/NVIDIA/ccluster/pkg/client`: a typed
  clientset with server-side apply and a fake that tracks field ownership, apply
  configurations with `Extract` functions, listers and a shared informer factory for
  `CCLInfraPlane`, `CCLComponentSet` and `CCLClusterConfig`.

### Changed
- **Breaking:** The API's Go package moves from `github.com/NVIDIA/ccluster/api/v1alpha1`
  to `github.com/NVIDIA/ccluster/api/ccluster/v1alpha1`, the group/version layout the
  Kubernetes code generators expect. Go code that imports it changes its import path; the
  CRDs and their `apiVersion` are unchanged.
