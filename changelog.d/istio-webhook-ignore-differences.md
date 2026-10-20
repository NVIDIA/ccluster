### Fixed
- The `istio` cluster_base component now reaches `Synced`, so the components after it in a
  CCLComponentSet are released. The cp operator tells Argo CD to ignore, and not revert,
  the `failurePolicy` that istiod sets on its two validating webhooks once it is serving.
