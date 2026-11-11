### Added
- `ccluster server` serves a read-only dashboard of the control plane saved by
  `ccluster init` at `http://127.0.0.1:8484` and opens it in the browser. It summarises
  infra planes by availability and verification and component sets by status (finished,
  rolling out, failed, terminating), and tables both kinds, with each set's components
  in rollout order by health, followed by any removed component whose chart is still
  being uninstalled. It refreshes every five seconds and keeps the last data with a
  notice when a refresh fails. Without `-n` it reads across the cluster and
  offers every namespace holding either kind; without cluster-wide read access it says
  so and suggests `-n`. With `-n` it reads only that namespace. It reads with the
  kubeconfig context's credentials, listens only on 127.0.0.1, ::1 or localhost, and
  answers only requests addressed to one of those names. The UI is built with React and
  NVIDIA's Kaizen UI design system in the style of NVIDIA's platform UI, with a light/dark toggle
  that starts from the system setting, and is embedded in the binary, so the release
  tarballs and `go install` include it.
