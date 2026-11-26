### Fixed
- The Go and Python `ccluster register` commands wait for the control plane to confirm the submitted credentials revision, so a previous successful registration cannot mask a failed re-registration.
