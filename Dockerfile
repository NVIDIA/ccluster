# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
# Build the operator images and the compute-ssh launcher image.
#
# --platform=$BUILDPLATFORM pins this stage to the machine doing the build, so under
# `docker buildx build --platform linux/amd64,linux/arm64` Go cross-compiles natively via
# TARGETOS/TARGETARCH instead of running once per platform under emulation. Plain
# `docker build` sets BUILDPLATFORM to the host and is unaffected.
FROM --platform=$BUILDPLATFORM golang:1.27.2 AS builder
ARG TARGETOS
ARG TARGETARCH
# PKG selects the Go package to build: ./cmd/ccluster-cp-operator,
# ./cmd/ccluster-ip-operator or ./catalogs/deployments/compute_deployment/compute-ssh. The
# resulting binary is always named "manager"; the compute-ssh stage renames it.
ARG PKG=./cmd/ccluster-cp-operator

WORKDIR /workspace
# Copy the Go Modules manifests
COPY go.mod go.mod
COPY go.sum go.sum
# cache deps before building and copying source so that we don't need to re-download as much
# and so that source changes don't invalidate our downloaded layer
# The module and build caches are BuildKit cache mounts: they persist on the builder
# across builds, so the second image (ip after cp) and every rebuild recompile only what
# changed instead of all of controller-runtime. In CI the mounts are saved and restored
# between runs by reproducible-containers/buildkit-cache-dance (see main.yml).
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

# Copy the Go source (relies on .dockerignore to filter)
COPY . .

# Build. GOARCH is left empty when TARGETARCH is unset (plain `docker build`) so the
# binary matches the host. No `-a`: it forced recompiling the standard library and every
# dependency on each build and defeated the caches above. -trimpath and -s -w drop build
# paths and debug symbols, which are noise in a distroless image.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o manager ${PKG}

# Collect the license and NOTICE files of every Go module linked into the binary, plus the
# Go standard library's, for /licenses/go in the image. go-licenses is installed for the
# build platform and analyses for the target one, and it fails on a license it cannot
# classify, so a new dependency needs a known license to ship.
ARG GO_LICENSES_VERSION=v2.0.1
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go install github.com/google/go-licenses/v2@${GO_LICENSES_VERSION} && \
    CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} \
    go-licenses save ${PKG} --save_path licenses/go --ignore github.com/NVIDIA/ccluster && \
    mkdir -p licenses/go/stdlib && cp "$(go env GOROOT)/LICENSE" licenses/go/stdlib/

# The compute-ssh launcher, built with
# --build-arg PKG=./catalogs/deployments/compute_deployment/compute-ssh --target compute-ssh.
# compute_deployment's init container runs `/compute-ssh install <path>`, so the binary
# path is part of the chart's contract.
FROM nvcr.io/nvidia/distroless/static:v4.1.5 AS compute-ssh
# REVISION is the commit, passed by the Makefile. The base image carries its own OCI
# labels, so every one it sets is overridden here; see the operator stage below.
ARG REVISION=unknown
LABEL org.opencontainers.image.source="https://github.com/NVIDIA/ccluster" \
      org.opencontainers.image.url="https://github.com/NVIDIA/ccluster" \
      org.opencontainers.image.documentation="https://github.com/NVIDIA/ccluster" \
      org.opencontainers.image.revision="${REVISION}" \
      org.opencontainers.image.version="" \
      org.opencontainers.image.created="" \
      org.opencontainers.image.title="ccluster compute-ssh" \
      org.opencontainers.image.description="SSH server that compute_deployment copies into the user container" \
      org.opencontainers.image.licenses="Apache-2.0"
WORKDIR /
COPY --from=builder /workspace/manager /compute-ssh
COPY LICENSE THIRD_PARTY_NOTICES.md /licenses/
COPY --from=builder /workspace/licenses/go /licenses/go
USER 65532:65532

ENTRYPOINT ["/compute-ssh"]

# Use distroless as minimal base image to package the manager binary. This is the last
# stage, so a build without --target produces an operator image.
# Both runtime stages use NVIDIA's OSRB-approved distroless build from nvcr.io rather
# than upstream gcr.io/distroless. Its default user is a named one and its default
# workdir is /app, so each stage sets WORKDIR and a numeric USER for runAsNonRoot.
# Refer to https://developer.download.nvidia.com/distroless-oss/index.html for more details
FROM nvcr.io/nvidia/distroless/static:v4.1.5
# org.opencontainers.image.source is what links the GHCR package to the repository, so
# the package inherits the repository's visibility and shows up on its Packages page.
# The base image sets its own url, documentation, revision, version, created and title
# labels, which would otherwise describe distroless rather than ccluster, so each is
# overridden. A Dockerfile cannot remove an inherited label, only overwrite it. version
# and created are left empty: a release retags the image built for its commit rather
# than rebuilding it, so a version baked in at build time would be wrong, and the tag
# carries it; a build timestamp would make every build of the same commit differ.
ARG REVISION=unknown
LABEL org.opencontainers.image.source="https://github.com/NVIDIA/ccluster" \
      org.opencontainers.image.url="https://github.com/NVIDIA/ccluster" \
      org.opencontainers.image.documentation="https://github.com/NVIDIA/ccluster" \
      org.opencontainers.image.revision="${REVISION}" \
      org.opencontainers.image.version="" \
      org.opencontainers.image.created="" \
      org.opencontainers.image.title="ccluster operator" \
      org.opencontainers.image.description="ccluster control-plane / infra-plane operator (see --build-arg PKG)" \
      org.opencontainers.image.licenses="Apache-2.0"
WORKDIR /
COPY --from=builder /workspace/manager .
COPY LICENSE THIRD_PARTY_NOTICES.md /licenses/
COPY --from=builder /workspace/licenses/go /licenses/go
USER 65532:65532

ENTRYPOINT ["/manager"]
