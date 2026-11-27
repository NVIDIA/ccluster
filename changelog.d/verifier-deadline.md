### Changed
- A `cluster_base` Verifier that ignores its 30-second deadline no longer keeps the ip
  operator from writing `Verified`: a check still running a second past its deadline is
  reported as `timed out after 30s`. Until that call returns, later passes report the
  chart as `still running since an earlier pass` rather than calling its check again.
