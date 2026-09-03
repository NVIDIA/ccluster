# Build the manager binary.
#
# --platform=$BUILDPLATFORM pins this stage to the machine doing the build, so under
# `docker buildx build --platform linux/amd64,linux/arm64` Go cross-compiles natively via
# TARGETOS/TARGETARCH instead of running once per platform under emulation. Plain
# `docker build` sets BUILDPLATFORM to the host and is unaffected.
FROM --platform=$BUILDPLATFORM golang:1.25 AS builder
ARG TARGETOS
ARG TARGETARCH
# CMD selects which operator to build: ccluster-cp-operator or ccluster-ip-operator.
# The resulting binary is always named "manager" so the ENTRYPOINT below is shared.
ARG CMD=ccluster-cp-operator

WORKDIR /workspace
# Copy the Go Modules manifests
COPY go.mod go.mod
COPY go.sum go.sum
# cache deps before building and copying source so that we don't need to re-download as much
# and so that source changes don't invalidate our downloaded layer
RUN go mod download

# Copy the Go source (relies on .dockerignore to filter)
COPY . .

# Build
# the GOARCH has no default value to allow the binary to be built according to the host where the command
# was called. For example, if we call make docker-build in a local env which has the Apple Silicon M1 SO
# the docker BUILDPLATFORM arg will be linux/arm64 when for Apple x86 it will be linux/amd64. Therefore,
# by leaving it empty we can ensure that the container and binary shipped on it will have the same platform.
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -a -o manager ./cmd/${CMD}

# Use distroless as minimal base image to package the manager binary
# Refer to https://github.com/GoogleContainerTools/distroless for more details
FROM gcr.io/distroless/static:nonroot
# org.opencontainers.image.source is what links the GHCR package to the repository, so
# the package inherits the repository's visibility and shows up on its Packages page.
LABEL org.opencontainers.image.source="https://github.com/NVIDIA/ccluster" \
      org.opencontainers.image.title="ccluster operator" \
      org.opencontainers.image.description="ccluster control-plane / infra-plane operator (see --build-arg CMD)" \
      org.opencontainers.image.licenses="Apache-2.0"
WORKDIR /
COPY --from=builder /workspace/manager .
USER 65532:65532

ENTRYPOINT ["/manager"]
