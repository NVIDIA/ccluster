### Security
- `go.mod` requires `github.com/klauspost/compress` v1.20.5, past the zstd decoder panic in
  CVE-2026-116591. No binary built from this repository linked the affected code; the
  upgrade clears dependency scanners that read `go.sum`.
