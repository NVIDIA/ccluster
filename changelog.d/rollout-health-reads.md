### Changed
- The cp operator no longer reconciles a `CCLComponentSet` when Argo CD's periodic refresh
  of one of its Applications changes nothing but the refresh time. Before, every refresh
  made the rollout and the component health controller judge the set again. A rollout pass
  that could not read a component's resource tree now tries again after 30 seconds, which
  the next refresh used to do.
- The rollout judges component health in spec order and stops at the first component that
  is not finished, so a pass reads no resource tree past it. All the reads of one pass share
  one 10-second deadline.
