### Changed
- The operator and `compute-ssh` images are built on NVIDIA's distroless base,
  `nvcr.io/nvidia/distroless/static:v4.1.4`, instead of upstream
  `gcr.io/distroless/static:nonroot`. They still run as UID 65532.
