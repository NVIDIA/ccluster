### Changed
- The operator and `compute-ssh` images are built on NVIDIA's distroless base,
  `nvcr.io/nvidia/distroless/static:v4.1.4`, instead of upstream
  `gcr.io/distroless/static:nonroot`. They still run as UID 65532.
  Their OCI labels describe ccluster rather than the base image:
  `org.opencontainers.image.revision` is the commit the image was built from, and
  `version` and `created` are empty, because a release retags rather than rebuilds.
