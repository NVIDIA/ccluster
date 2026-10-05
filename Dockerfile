# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
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
    go build -trimpath -ldflags="-s -w" -o manager ./cmd/${CMD}

# Collect the license and NOTICE files of every Go module linked into the binary, plus the
# Go standard library's, for /licenses/go in the image. go-licenses is installed for the
# build platform and analyses for the target one, and it fails on a license it cannot
# classify, so a new dependency needs a known license to ship.
ARG GO_LICENSES_VERSION=v2.0.1
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go install github.com/google/go-licenses/v2@${GO_LICENSES_VERSION} && \
    CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} \
    go-licenses save ./cmd/${CMD} --save_path licenses/go --ignore github.com/NVIDIA/ccluster && \
    mkdir -p licenses/go/stdlib && cp "$(go env GOROOT)/LICENSE" licenses/go/stdlib/

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
COPY LICENSE THIRD_PARTY_NOTICES.md /licenses/
COPY --from=builder /workspace/licenses/go /licenses/go
USER 65532:65532

ENTRYPOINT ["/manager"]
