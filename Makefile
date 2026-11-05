# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
# Image URLs to use for all building/pushing image targets. The two operators and the
# compute-ssh launcher ship as separate images, built from the same Dockerfile via
# --build-arg PKG, the Go package path.
#
# Every image is tagged with the commit it was built from - there is no :dev or :latest.
# A release never rebuilds: `make docker-retag VERSION=X.Y.Z` adds the version tag to the
# :<sha> manifest that CI already pushed for that commit, so the released image is
# bit-for-bit the one that was built and tested. A dirty working tree gets a -dirty
# suffix so it cannot masquerade as a commit. The kind loop (docker-build-*, load-*,
# deploy-*) is unaffected: a locally built ghcr.io/... :<sha> image is loaded and
# deployed by that name and never pulled (IfNotPresent, non-latest tag).
GIT_SHA   ?= $(shell git rev-parse --short=12 HEAD 2>/dev/null || echo unknown)$(shell git diff --quiet HEAD 2>/dev/null || echo -dirty)
IMG_TAG   ?= $(GIT_SHA)
# VERSION is the release version (the git tag without its leading v). Set by the release
# workflow; empty in day-to-day use. Targets that publish refuse to run without it.
VERSION   ?=
# RELEASE_VERSION is the version recorded in the VERSION file at the repository root: the
# one place a release bumps. Nothing stamps from it - artifacts are stamped from VERSION,
# i.e. the tag - but `make check-version` refuses a release whose tag disagrees with it,
# which is what ties a tag to the commit that carries its changelog.
RELEASE_VERSION := $(strip $(shell cat VERSION 2>/dev/null))
# NEXT_VERSION is RELEASE_VERSION with the patch bumped: the version a main-branch build
# is heading towards, used as the base of the dev chart version so an unreleased build
# sorts after the release it follows and before the one it precedes.
NEXT_VERSION := $(shell printf '%s' '$(or $(RELEASE_VERSION),0.0.0)' | awk -F. '{printf "%d.%d.%d", $$1, $$2, $$3+1}')
# DEV_NUMBER is the number of commits up to HEAD. main is squash-merged and linear, so it
# grows with every merge and names one commit: the Python package, whose PEP 440 version
# cannot carry a hash, uses it as <next>.dev<N> where the charts use <next>-dev.<sha>.
DEV_NUMBER := $(shell git rev-list --count HEAD 2>/dev/null || echo 0)
# REGISTRY is the OCI prefix for both the images and the charts (oci://$(REGISTRY)/charts).
REGISTRY  ?= ghcr.io/nvidia
IMG_CP    ?= $(REGISTRY)/ccluster-cp-operator:$(IMG_TAG)
IMG_IP    ?= $(REGISTRY)/ccluster-ip-operator:$(IMG_TAG)
IMG_COMPUTE_SSH ?= $(REGISTRY)/ccluster-compute-ssh:$(IMG_TAG)
# Platforms docker-buildx-* builds for. Only what is actually run and tested.
PLATFORMS ?= linux/amd64,linux/arm64
# Extra flags for docker-buildx-*; CI passes layer-cache flags (--cache-from/--cache-to).
BUILDX_ARGS ?=
# Build args every image build passes. REVISION is the commit, stamped into the
# org.opencontainers.image.revision label; a release retag keeps it, as the commit is the same.
IMAGE_BUILD_ARGS = --build-arg REVISION=$(GIT_SHA)

# GOTOOLCHAIN pins every go command a recipe runs to the toolchain line in go.mod, so
# local builds, tests, lint, generators and tool installs use the same Go as CI (setup-go
# reads that line) and the images (check-go-version holds the Dockerfile to it). Without
# it a newer installed Go runs as-is, since go.mod's go line is only a minimum. The go
# command downloads the pinned toolchain on first use.
export GOTOOLCHAIN := $(shell sed -n 's/^toolchain //p' go.mod)

# CONTAINER_TOOL defines the container tool to be used for building images.
# Be aware that the target commands are only tested with Docker which is
# scaffolded by default. However, you might want to replace it to use other
# tools. (i.e. podman)
CONTAINER_TOOL ?= docker

# Setting SHELL to bash allows bash commands to be executed by recipes.
# Options are set to exit when a recipe line exits non-zero or a piped command fails.
SHELL = /usr/bin/env bash -o pipefail
.SHELLFLAGS = -ec

.PHONY: all
all: init build load deploy ## Create both dev Kind clusters, build everything, load the images and deploy both operators.

##@ General

# The help target prints out all targets with their descriptions organized
# beneath their categories. The categories are represented by '##@' and the
# target descriptions by '##'. The awk command is responsible for reading the
# entire set of makefiles included in this invocation, looking for lines of the
# file as xyz: ## something, and then pretty-format the target and help. Then,
# if there's a line with ##@ something, that gets pretty-printed as a category.
# More info on the usage of ANSI control characters for terminal formatting:
# https://en.wikipedia.org/wiki/ANSI_escape_code#SGR_parameters
# More info on the awk command:
# http://linuxcommand.org/lc3_adv_awk.php

.PHONY: help
help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

##@ Development

.PHONY: manifests
manifests: crd-generate controller-gen ## Generate WebhookConfiguration, ClusterRole and CustomResourceDefinition objects.
	# RBAC once per operator, so each ClusterRole holds only that operator's rules.
	# The role name is the same in both; the overlay namePrefix disambiguates them
	# into ccluster-cp-manager-role and ccluster-ip-manager-role, which is what lets
	# config/{cp,ip}/rbac/role_binding.yaml be identical.
	"$(CONTROLLER_GEN)" rbac:roleName=manager-role paths="./internal/controller/cp/..." \
		output:rbac:artifacts:config=config/cp/rbac
	"$(CONTROLLER_GEN)" rbac:roleName=manager-role paths="./internal/controller/ip/..." \
		output:rbac:artifacts:config=config/ip/rbac

# CRDs once, over every API group. It loads only ./api/..., never the operators, so
# client-generate can depend on it even when pkg/client is missing.
.PHONY: crd-generate
crd-generate: controller-gen ## Generate the CustomResourceDefinition and WebhookConfiguration objects from ./api.
	"$(CONTROLLER_GEN)" crd webhook paths="./api/..." output:crd:artifacts:config=config/crd/bases

.PHONY: generate
generate: controller-gen ## Generate code containing DeepCopy, DeepCopyInto, and DeepCopyObject method implementations.
	"$(CONTROLLER_GEN)" object:headerFile="hack/boilerplate.go.txt" paths="./..."

# The API packages the Go client is generated from, as import paths. Only kinds marked
# +genclient are included.
API_PACKAGES = $(shell go list ./api/...)
CLIENT_PACKAGE = $(shell go list -m)/pkg/client
# The OpenAPI schema hack/clientschema builds from the CRDs for applyconfiguration-gen.
CLIENT_SCHEMA ?= dist/clientschema.json
# Where the generators write before the result replaces pkg/client, so a failed run leaves
# the last client in place: internal/remote imports it, and without it controller-gen
# cannot load the cp operator's packages for make manifests, make test or the builds. The
# leading underscore keeps ./... patterns away from a half-generated copy.
CLIENT_STAGING ?= dist/_client

# It needs only the CRDs, so it depends on crd-generate rather than manifests: manifests'
# RBAC step loads the cp operator, which imports pkg/client, so a missing pkg/client could
# not otherwise be regenerated.
.PHONY: client-generate
client-generate: crd-generate applyconfiguration-gen client-gen lister-gen informer-gen ## Regenerate the Go clientset, listers, informers and apply configurations in pkg/client.
	rm -rf "$(CLIENT_STAGING)"
	go run ./hack/clientschema --crds "$(CRD_DIR)" --out "$(CLIENT_SCHEMA)"
	# Apply configurations give the clientset its Apply methods and the fake clientset its
	# field-managed NewClientset; without them only the deprecated NewSimpleClientset exists.
	# The fake tracks field ownership with the schema and fails every write without it.
	"$(APPLYCONFIGURATION_GEN)" --openapi-schema "$(CLIENT_SCHEMA)" \
		--output-dir "$(CLIENT_STAGING)/applyconfiguration" \
		--output-pkg $(CLIENT_PACKAGE)/applyconfiguration \
		--go-header-file hack/boilerplate.go.txt $(API_PACKAGES)
	"$(CLIENT_GEN)" --clientset-name versioned --input-base "" $(addprefix --input ,$(API_PACKAGES)) \
		--apply-configuration-package $(CLIENT_PACKAGE)/applyconfiguration \
		--output-dir "$(CLIENT_STAGING)/clientset" --output-pkg $(CLIENT_PACKAGE)/clientset \
		--go-header-file hack/boilerplate.go.txt
	"$(LISTER_GEN)" --output-dir "$(CLIENT_STAGING)/listers" --output-pkg $(CLIENT_PACKAGE)/listers \
		--go-header-file hack/boilerplate.go.txt $(API_PACKAGES)
	"$(INFORMER_GEN)" --versioned-clientset-package $(CLIENT_PACKAGE)/clientset/versioned \
		--listers-package $(CLIENT_PACKAGE)/listers \
		--output-dir "$(CLIENT_STAGING)/informers" --output-pkg $(CLIENT_PACKAGE)/informers \
		--go-header-file hack/boilerplate.go.txt $(API_PACKAGES)
	rm -rf pkg/client
	mv "$(CLIENT_STAGING)" pkg/client

.PHONY: fmt
fmt: ## Run go fmt against code.
	go fmt ./...

.PHONY: vet
vet: ## Run go vet against code.
	go vet ./...

.PHONY: test
test: manifests generate fmt vet setup-envtest ## Run tests.
	KUBEBUILDER_ASSETS="$(shell "$(ENVTEST)" use $(ENVTEST_K8S_VERSION) --bin-dir "$(LOCALBIN)" -p path)" go test $$(go list ./... | grep -v /e2e) -coverprofile cover.out

# TODO(user): To use a different vendor for e2e tests, modify the setup under 'tests/e2e'.
# The default setup assumes Kind is pre-installed and builds/loads the Manager Docker image locally.
# CertManager is installed by default; skip with:
# - CERT_MANAGER_INSTALL_SKIP=true
# The two clusters the e2e targets create, use and then destroy: a control plane and a
# separate infra plane, the same pair `make init` creates for development as
# KIND_CLUSTER_CP / KIND_CLUSTER_IP (below, in the Build section), but never those
# clusters - `make test-e2e` uninstalls operators and CRDs and deletes its clusters
# when it is done, which must never happen to ones you are working in.
KIND_CLUSTER_E2E_CP ?= ccluster-test-e2e-cp
KIND_CLUSTER_E2E_IP ?= ccluster-test-e2e-ip

# The ip cluster is created first, so `kind create` leaves the current kubeconfig
# context on the cp cluster, which is the one the e2e specs use by default.
.PHONY: setup-test-e2e
setup-test-e2e: check-kind-host ## Set up the two Kind clusters for e2e tests if they do not exist.
	$(call kind-create-cluster,$(KIND_CLUSTER_E2E_IP))
	$(call kind-create-cluster,$(KIND_CLUSTER_E2E_CP))

# Set E2E_FULL=true (or use test-e2e-full) to run every e2e spec. The quick tier
# (default) skips the specs labelled "full" - the ones that need real Argo CD: a real
# rollout, multi-component ordered release, reverse teardown, drift/selfHeal correction,
# a values change rolling forward, the field ownership contract with Argo CD, the
# AppProject tenancy check, and istio converging despite istiod's webhook patches. The full tier runs everything: the suite always deploys
# without Argo CD first and runs the quick specs, then the "with real Argo CD" container
# in test/e2e/e2e_test.go upgrades the release to add Argo CD and runs the rest. So the
# full tier is a strict superset of the quick one.
E2E_FULL ?= false
# go test kills the binary after 10m by default. The quick tier fits; the full tier does
# not once the Argo CD and catalog images are pulled cold, as they are in CI, and the
# panic that results looks like a hang in the last spec rather than a timeout.
E2E_TIMEOUT ?= $(if $(filter true,$(E2E_FULL)),50m,20m)

.PHONY: test-e2e
test-e2e: setup-test-e2e manifests generate fmt vet ## Run the quick e2e tests (no Argo CD). Set E2E_FULL=true, or use test-e2e-full, to run everything.
	KIND=$(KIND) KIND_CLUSTER=$(KIND_CLUSTER_E2E_CP) KIND_CLUSTER_IP=$(KIND_CLUSTER_E2E_IP) \
		go test -tags=e2e ./test/e2e/ -v -ginkgo.v -timeout $(E2E_TIMEOUT) \
		$(if $(filter true,$(E2E_FULL)),,-ginkgo.label-filter="!full")
	$(MAKE) cleanup-test-e2e

.PHONY: test-e2e-full
test-e2e-full: ## Run every e2e test: the quick tier, then real Argo CD rollout, ordered release, reverse teardown, drift correction, values roll-forward, tenancy and istio's ignored drift. Slower; explicit only.
	$(MAKE) test-e2e E2E_FULL=true

.PHONY: cleanup-test-e2e
cleanup-test-e2e: kind ## Tear down the two Kind clusters used for e2e tests.
	@$(KIND) delete cluster --name $(KIND_CLUSTER_E2E_CP)
	@$(KIND) delete cluster --name $(KIND_CLUSTER_E2E_IP)

.PHONY: lint
lint: golangci-lint ## Run golangci-lint linter
	"$(GOLANGCI_LINT)" run

.PHONY: lint-fix
lint-fix: golangci-lint ## Run golangci-lint linter and perform fixes
	"$(GOLANGCI_LINT)" run --fix

.PHONY: lint-config
lint-config: golangci-lint ## Verify golangci-lint linter configuration
	"$(GOLANGCI_LINT)" config verify

.PHONY: check-headers
check-headers: ## Fail if a tracked source file lacks the SPDX license header.
	hack/check-headers.sh

# Dependabot bumps the Dockerfile's golang image but not go.mod, so each such PR fails
# here until it updates the toolchain line to match.
.PHONY: check-go-version
check-go-version: ## Fail unless the Dockerfile's golang image matches the toolchain line in go.mod.
	@[ -n "$(GOTOOLCHAIN)" ] || { echo "go.mod has no toolchain line, e.g. 'toolchain go1.27.2'" >&2; exit 1; }
	@image="$$(sed -n 's/^FROM .*golang:\([^ @]*\).*/\1/p' Dockerfile)"; \
	[ "go$$image" = "$(GOTOOLCHAIN)" ] || { echo "Dockerfile builds with golang:$$image but go.mod has 'toolchain $(GOTOOLCHAIN)': change both together" >&2; exit 1; }

##@ Build

# Everything a deploy consumes, in dependency order: generated code and manifests
# (pulled in by build-cp/build-ip), both binaries, both images, and both charts. It
# does not touch a cluster - `kind load docker-image` and `make deploy-{cp,ip}` are
# still separate, per the rule that every cluster-touching target is suffixed.
# client-generate runs first: generate loads every package, and a pkg/client generated from
# API types that have since changed may no longer compile.
.PHONY: build
build: client-generate build-cp build-ip build-cli docker-build helm-generate python-generate ## Build everything: manifests, CRDs, deepcopy, the Go client, both operator binaries, the CLI, both images, both charts and the Python models.

.PHONY: build-cp
build-cp: manifests generate fmt vet ## Build the ccluster-cp-operator binary.
	go build -o bin/ccluster-cp-operator ./cmd/ccluster-cp-operator

.PHONY: build-ip
build-ip: manifests generate fmt vet ## Build the ccluster-ip-operator binary.
	go build -o bin/ccluster-ip-operator ./cmd/ccluster-ip-operator

# The ccluster CLI reports v$(CLI_VERSION) (see the Release section), with the v of the
# git tag, so a release binary reports what one built by `go install ...@vX.Y.Z` does:
# its module version.
CLI_LDFLAGS = -X github.com/NVIDIA/ccluster/internal/cli.version=v$(CLI_VERSION)

.PHONY: build-cli
build-cli: generate fmt vet ## Build the ccluster CLI as bin/ccluster.
	go build -ldflags "$(CLI_LDFLAGS)" -o bin/ccluster ./cmd/ccluster

.PHONY: run-cp
run-cp: manifests generate fmt vet ## Run ccluster-cp-operator from your host.
	go run ./cmd/ccluster-cp-operator --argocd-namespace=$(or $(ARGOCD_NAMESPACE),argocd) \
		--dns-endpoint-namespace=$(HELM_NAMESPACE_CP) --base-domain=$(BASE_DOMAIN)

.PHONY: run-ip
run-ip: manifests generate fmt vet ## Run ccluster-ip-operator from your host.
	go run ./cmd/ccluster-ip-operator

# If you wish to build the manager image targeting other platforms you can use the --platform flag.
# (i.e. docker build --platform linux/arm64). However, you must enable docker buildKit for it.
# More info: https://docs.docker.com/develop/develop-images/build_enhancements/
.PHONY: docker-build
docker-build: docker-build-cp docker-build-ip ## Build both manager images.

.PHONY: docker-build-cp
docker-build-cp: ## Build docker image with the ccluster-cp-operator manager.
	$(CONTAINER_TOOL) build $(IMAGE_BUILD_ARGS) --build-arg PKG=./cmd/ccluster-cp-operator -t ${IMG_CP} .

.PHONY: docker-build-ip
docker-build-ip: ## Build docker image with the ccluster-ip-operator manager.
	$(CONTAINER_TOOL) build $(IMAGE_BUILD_ARGS) --build-arg PKG=./cmd/ccluster-ip-operator -t ${IMG_IP} .

# The launcher is not part of docker-build: no operator deploy needs it, and
# compute_deployment pulls it from IMG_COMPUTE_SSH's registry. Its source sits beside the
# chart that runs it.
COMPUTE_SSH_PKG := ./catalogs/deployments/compute_deployment/compute-ssh
.PHONY: docker-build-compute-ssh
docker-build-compute-ssh: ## Build the compute-ssh launcher image.
	$(CONTAINER_TOOL) build $(IMAGE_BUILD_ARGS) --build-arg PKG=$(COMPUTE_SSH_PKG) --target compute-ssh -t ${IMG_COMPUTE_SSH} .

# A locally built image is invisible to a Kind cluster until it is loaded onto the
# node - that, not a bad tag, is the usual cause of ErrImagePull on a `:dev` image.
# Loading does not restart anything: the Deployment spec is unchanged, so finish with
#   kubectl -n ccluster-cp-system rollout restart deploy/ccluster-cp-controller-manager
#
# The two development clusters, created by `make init`. They are separate because the
# operators are: cp runs Argo CD and the control plane, ip is a cluster it registers.
KIND_CLUSTER_CP ?= ccluster-cp
KIND_CLUSTER_IP ?= ccluster-ip

.PHONY: load
load: load-cp load-ip ## Load each manager image into its development Kind cluster.

.PHONY: load-cp
load-cp: docker-build-cp kind ## Load the ccluster-cp-operator image into KIND_CLUSTER_CP.
	$(KIND) load docker-image $(IMG_CP) --name $(KIND_CLUSTER_CP)

.PHONY: load-ip
load-ip: docker-build-ip kind ## Load the ccluster-ip-operator image into KIND_CLUSTER_IP.
	$(KIND) load docker-image $(IMG_IP) --name $(KIND_CLUSTER_IP)

.PHONY: docker-push
docker-push: docker-push-cp docker-push-ip ## Push both manager images.

.PHONY: docker-push-cp
docker-push-cp: ## Push the ccluster-cp-operator docker image.
	$(CONTAINER_TOOL) push ${IMG_CP}

.PHONY: docker-push-ip
docker-push-ip: ## Push the ccluster-ip-operator docker image.
	$(CONTAINER_TOOL) push ${IMG_IP}

# Multi-platform builds. These push straight to the registry (a multi-arch manifest list
# cannot be loaded into the local docker image store), always under the :<sha> tag, and
# need a buildx builder with the docker-container driver: `docker buildx create --use`
# locally, docker/setup-buildx-action in CI. The Dockerfile already carries
# --platform=$BUILDPLATFORM on its build stage, so no rewritten copy is needed.
.PHONY: docker-buildx
docker-buildx: docker-buildx-cp docker-buildx-ip docker-buildx-compute-ssh ## Build and push all images for PLATFORMS.

.PHONY: docker-buildx-cp
docker-buildx-cp: ## Build and push the multi-platform ccluster-cp-operator image.
	$(CONTAINER_TOOL) buildx build --push --platform=$(PLATFORMS) $(IMAGE_BUILD_ARGS) --build-arg PKG=./cmd/ccluster-cp-operator -t ${IMG_CP} $(BUILDX_ARGS) .

.PHONY: docker-buildx-ip
docker-buildx-ip: ## Build and push the multi-platform ccluster-ip-operator image.
	$(CONTAINER_TOOL) buildx build --push --platform=$(PLATFORMS) $(IMAGE_BUILD_ARGS) --build-arg PKG=./cmd/ccluster-ip-operator -t ${IMG_IP} $(BUILDX_ARGS) .

.PHONY: docker-buildx-compute-ssh
docker-buildx-compute-ssh: ## Build and push the multi-platform compute-ssh launcher image.
	$(CONTAINER_TOOL) buildx build --push --platform=$(PLATFORMS) $(IMAGE_BUILD_ARGS) --build-arg PKG=$(COMPUTE_SSH_PKG) --target compute-ssh -t ${IMG_COMPUTE_SSH} $(BUILDX_ARGS) .

# Releasing an image is a retag, not a rebuild. `imagetools create` copies the manifest
# list already in the registry under a second tag, server-side: nothing is pulled, and the
# :X.Y.Z image is by construction the :<sha> image that CI built for the tagged commit.
.PHONY: docker-retag
docker-retag: docker-retag-cp docker-retag-ip docker-retag-compute-ssh ## Retag all :<sha> images in the registry as :$(VERSION).

.PHONY: docker-retag-cp
docker-retag-cp: require-version ## Retag the pushed ccluster-cp-operator:<sha> image as :$(VERSION).
	$(CONTAINER_TOOL) buildx imagetools create -t $(REGISTRY)/ccluster-cp-operator:$(VERSION) ${IMG_CP}

.PHONY: docker-retag-ip
docker-retag-ip: require-version ## Retag the pushed ccluster-ip-operator:<sha> image as :$(VERSION).
	$(CONTAINER_TOOL) buildx imagetools create -t $(REGISTRY)/ccluster-ip-operator:$(VERSION) ${IMG_IP}

.PHONY: docker-retag-compute-ssh
docker-retag-compute-ssh: require-version ## Retag the pushed ccluster-compute-ssh:<sha> image as :$(VERSION).
	$(CONTAINER_TOOL) buildx imagetools create -t $(REGISTRY)/ccluster-compute-ssh:$(VERSION) ${IMG_COMPUTE_SSH}

.PHONY: require-version
require-version:
	@[ -n "$(VERSION)" ] || { echo "VERSION is required, e.g. make $(MAKECMDGOALS) VERSION=0.1.0" >&2; exit 1; }

.PHONY: check-version
check-version: require-version ## Fail unless VERSION matches the VERSION file (the release tag must be cut on the release commit).
	@[ "$(VERSION)" = "$(RELEASE_VERSION)" ] || { echo "VERSION=$(VERSION) but the VERSION file says '$(RELEASE_VERSION)': tag the commit that carries the release PR" >&2; exit 1; }

.PHONY: build-installer
build-installer: build-installer-cp build-installer-ip ## Generate consolidated install YAML for both operators.

# dist/install-*.yaml is input for the chart generator, not a deploy path. The image
# it carries is never consumed: the deployed image is manager.image in the chart's
# values.yaml, which helm-generate preserves and helm-package-* stamps. The image name
# is committed in the overlays and nothing here rewrites them.
.PHONY: build-installer-cp
build-installer-cp: manifests generate kustomize ## Generate dist/install-cp.yaml (CRDs + cp deployment).
	mkdir -p dist
	"$(KUSTOMIZE)" build config/cp > dist/install-cp.yaml

.PHONY: build-installer-ip
build-installer-ip: manifests generate kustomize ## Generate dist/install-ip.yaml (ip deployment, no CRDs).
	mkdir -p dist
	"$(KUSTOMIZE)" build config/ip > dist/install-ip.yaml

##@ Deployment

# Bring up the two development clusters. Idempotent: an existing cluster is left alone,
# so this is safe to re-run.
#
# `kind create cluster` switches the current kubeconfig context to whatever it just
# made, so ip is created first and cp last - the context is left on the cluster most
# work happens in. Never rely on that: check with `kubectl config current-context`
# before any target that touches a cluster.
.PHONY: init
init: init-ip init-cp ## Create both development Kind clusters. Leaves the kubeconfig context on the cp cluster.
	@echo "Clusters ready. cp: kind-$(KIND_CLUSTER_CP)  ip: kind-$(KIND_CLUSTER_IP)"
	@echo "Current kubeconfig context: $$("$(KUBECTL)" config current-context)"

.PHONY: init-cp
init-cp: check-kind-host ## Create the control-plane development Kind cluster if it does not exist.
	$(call kind-create-cluster,$(KIND_CLUSTER_CP))

.PHONY: init-ip
init-ip: check-kind-host ## Create the infra-plane development Kind cluster if it does not exist.
	$(call kind-create-cluster,$(KIND_CLUSTER_IP))

# Every kind node shares the host kernel's per-user inotify limits. Below these, kind's
# recommended values, the full e2e tier exhausts them and istio-cni-node crash-loops with
# "watcher create: too many open files". The quick tier installs no catalog charts and
# fits under a stock Linux default. This prints the environment that local and CI runs
# must agree on, then warns on a host that is short, failing only with E2E_FULL=true.
# It never changes the host: the e2e workflow raises the limits with sudo first. macOS
# keeps the limits inside Docker's VM.
INOTIFY_MIN_INSTANCES ?= 512
INOTIFY_MIN_WATCHES ?= 524288

.PHONY: check-kind-host
check-kind-host: kind ## Print the kind environment and check the host's inotify limits, failing only with E2E_FULL=true.
	@echo "kind:       $$("$(KIND)" version)"
	@echo "node image: $(KIND_NODE_IMAGE)"
	@echo "docker:     $$(docker version --format '{{.Server.Version}}' 2>/dev/null || echo unavailable)"
	@echo "cpus:       $$(getconf _NPROCESSORS_ONLN)"
	@if [ -r /proc/sys/fs/inotify/max_user_instances ]; then \
		instances=$$(cat /proc/sys/fs/inotify/max_user_instances); \
		watches=$$(cat /proc/sys/fs/inotify/max_user_watches); \
		echo "inotify:    max_user_instances=$$instances max_user_watches=$$watches"; \
		if [ "$$instances" -lt $(INOTIFY_MIN_INSTANCES) ] || [ "$$watches" -lt $(INOTIFY_MIN_WATCHES) ]; then \
			echo "The inotify limits are below kind's recommended values, which the full e2e tier" >&2; \
			echo "and a cluster running catalog components such as istio need. Raise them with:" >&2; \
			echo "  sudo sysctl -w fs.inotify.max_user_instances=$(INOTIFY_MIN_INSTANCES) fs.inotify.max_user_watches=$(INOTIFY_MIN_WATCHES)" >&2; \
			$(if $(filter true,$(E2E_FULL)),exit 1,echo "Continuing: only the full e2e tier (E2E_FULL=true) fails on this." >&2); \
		fi; \
	else \
		echo "inotify:    not readable on this host; skipped"; \
	fi

# Destroys both development clusters and everything deployed to them. There is no
# confirmation, matching cleanup-test-e2e. `make init` builds them back empty.
.PHONY: clean
clean: clean-ip clean-cp ## Delete both development Kind clusters. Destructive.

.PHONY: clean-cp
clean-cp: kind ## Delete the control-plane development Kind cluster.
	$(KIND) delete cluster --name $(KIND_CLUSTER_CP)

.PHONY: clean-ip
clean-ip: kind ## Delete the infra-plane development Kind cluster.
	$(KIND) delete cluster --name $(KIND_CLUSTER_IP)

# Everything `clean` does, plus every build artifact: the compiled binaries and the
# downloaded tools in bin/, and the generated installers in dist/. The tools are
# re-downloaded on the next build, so this costs a slow first run.
#
# The locally built container images are left alone - `docker rmi $(IMG_CP) $(IMG_IP)`
# if you want those gone too.
.PHONY: clean-all
clean-all: clean ## Delete the development clusters and all build artifacts (bin/, dist/).
	rm -rf "$(LOCALBIN)" dist

# kind-create-cluster,<name> - create a Kind cluster unless it is already there. The
# name must match a whole line: one cluster's name can be a prefix of another's.
define kind-create-cluster
@if $(KIND) get clusters 2>/dev/null | grep -qxF '$(1)'; then \
	echo "Kind cluster '$(1)' already exists. Skipping creation."; \
else \
	echo "Creating Kind cluster '$(1)'..."; $(KIND) create cluster --name $(1) --image $(KIND_NODE_IMAGE); \
fi
endef

ifndef ignore-not-found
  ignore-not-found = false
endif

.PHONY: install
install: manifests kustomize ## Install CRDs into the K8s cluster specified in ~/.kube/config.
	@out="$$( "$(KUSTOMIZE)" build config/crd 2>/dev/null || true )"; \
	if [ -n "$$out" ]; then echo "$$out" | "$(KUBECTL)" apply -f -; else echo "No CRDs to install; skipping."; fi

.PHONY: uninstall
uninstall: manifests kustomize ## Uninstall CRDs from the K8s cluster specified in ~/.kube/config. Call with ignore-not-found=true to ignore resource not found errors during deletion.
	@out="$$( "$(KUSTOMIZE)" build config/crd 2>/dev/null || true )"; \
	if [ -n "$$out" ]; then echo "$$out" | "$(KUBECTL)" delete --ignore-not-found=$(ignore-not-found) -f -; else echo "No CRDs to delete; skipping."; fi

# Both operators deploy via Helm. The charts under charts/{cp,ip}/chart are
# generated from the kustomize overlays by `make helm-generate` — see the Helm
# section below.

# The operator looks for Argo CD in its own namespace, the release namespace, where
# the chart's argo-cd dependency installs it. Set ARGOCD_NAMESPACE for an existing
# Argo CD elsewhere. With the bundled Argo CD, deploy-cp also points the operator at its
# argocd-server Service. manager.args is restated in full because `--set manager.args[1]=...` builds
# a sparse list and nulls index 0, silently dropping --leader-elect.
# The one target that names both clusters: deploy-cp and deploy-ip on their own still
# act on the current kubeconfig context, so they can be pointed anywhere.
.PHONY: deploy
deploy: ## Deploy each operator to its own development Kind cluster.
	$(MAKE) deploy-cp KUBE_CONTEXT_CP=kind-$(KIND_CLUSTER_CP)
	$(MAKE) deploy-ip KUBE_CONTEXT_IP=kind-$(KIND_CLUSTER_IP)

.PHONY: deploy-cp
deploy-cp: helm helm-deps-cp ## Deploy ccluster-cp-operator (and Argo CD) to the K8s cluster specified in ~/.kube/config.
	"$(HELM)" upgrade --install $(HELM_RELEASE_CP) $(CHART_CP) $(HELM_CONTEXT_CP) \
		--namespace $(HELM_NAMESPACE_CP) --create-namespace \
		--set manager.image.repository=$(word 1,$(subst :, ,$(IMG_CP))) \
		--set manager.image.tag=$(word 2,$(subst :, ,$(IMG_CP))) \
		--set argo-cd.enabled=$(ARGOCD) \
		--set external-dns.enabled=$(DNS) \
		--set-json 'manager.args=["--leader-elect"$(if $(ARGOCD_NAMESPACE),$(COMMA)"--argocd-namespace=$(ARGOCD_NAMESPACE)"),"--catalog-repo-url=$(CATALOG_REPO_URL)","--catalog-revision=$(CATALOG_REVISION)","--base-domain=$(BASE_DOMAIN)"$(ARGOCD_SERVER_ARGS)]' \
		$(CATALOG_CREDENTIALS) \
		--wait --timeout $(HELM_TIMEOUT) $(HELM_ARGS)

.PHONY: undeploy-cp
undeploy-cp: helm ## Undeploy ccluster-cp-operator. CRDs are retained (helm.sh/resource-policy: keep).
	"$(HELM)" uninstall $(HELM_RELEASE_CP) $(HELM_CONTEXT_CP) \
		--namespace $(HELM_NAMESPACE_CP) --ignore-not-found

.PHONY: deploy-ip
deploy-ip: helm ## Deploy ccluster-ip-operator to the K8s cluster specified in ~/.kube/config.
	"$(HELM)" upgrade --install $(HELM_RELEASE_IP) $(CHART_IP) $(HELM_CONTEXT_IP) \
		--namespace $(HELM_NAMESPACE_IP) --create-namespace \
		--set manager.image.repository=$(word 1,$(subst :, ,$(IMG_IP))) \
		--set manager.image.tag=$(word 2,$(subst :, ,$(IMG_IP))) \
		--wait --timeout $(HELM_TIMEOUT) $(HELM_ARGS)

.PHONY: undeploy-ip
undeploy-ip: helm ## Undeploy ccluster-ip-operator.
	"$(HELM)" uninstall $(HELM_RELEASE_IP) $(HELM_CONTEXT_IP) \
		--namespace $(HELM_NAMESPACE_IP) --ignore-not-found

##@ Helm

# The operator charts are GENERATED, not hand-written: `kubebuilder edit
# --plugins=helm/v2-alpha` parses the kustomize output in dist/install-{cp,ip}.yaml
# and emits charts/{cp,ip}/chart. config/ therefore stays the single source of truth
# for the manager Deployment, RBAC and CRDs, and controller-gen output can never
# drift from what gets deployed.
#
# The plugin preserves Chart.yaml and values.yaml. Hand-edit only those two — the Argo
# CD dependency and the pinned fullnameOverride live there for exactly that reason.
# Everything under templates/ is generated: helm-generate deletes the directory first,
# because the plugin only skips, never removes, files it no longer emits (it left a
# stale templates/monitoring/ behind once) and without --force it also keeps an
# existing _helpers.tpl and NOTES.txt, which a newer plugin's templates may not match.
#
# Note the plugin records only ONE chart in PROJECT (the last invocation), so both
# charts are always regenerated here with explicit --manifests/--output-dir flags
# rather than by a bare `kubebuilder edit`.
#
# The generated charts are COMMITTED, so deploying does not require the kubebuilder
# CLI. Run `make helm-generate` after changing anything under config/, api/, or a
# +kubebuilder:rbac marker; CI fails if the committed charts are stale.

HELM ?= helm
HELM_RELEASE_CP ?= ccluster-cp
HELM_RELEASE_IP ?= ccluster-ip
HELM_NAMESPACE_CP ?= ccluster-cp-system
HELM_NAMESPACE_IP ?= ccluster-ip-system
CHART_CP ?= charts/cp/chart
CHART_IP ?= charts/ip/chart
HELM_TIMEOUT ?= 5m
# Set ARGOCD=false to install the cp operator without Argo CD, e.g. into a cluster
# that already runs it, or for hermetic tests that must not pull the Argo CD images.
ARGOCD ?= true
# The namespace of an existing Argo CD, for deploy-cp ARGOCD=false and run-cp. Empty
# means the operator's own namespace; run-cp, which has none, then uses `argocd`.
ARGOCD_NAMESPACE ?=
# A literal comma cannot appear inside $(if).
COMMA := ,
# The manager arg deploy-cp adds for the bundled Argo CD.
ARGOCD_SERVER_ARGS = $(if $(filter true,$(ARGOCD)),$(COMMA)"--argocd-server-service=ccluster-cp-argocd-server")
# Set DNS=true to install the chart's external-dns, which publishes each CCLInfraPlane's
# wildcard record. Its provider, zones and credentials come through HELM_ARGS, such as
# `-f dns-values.yaml` (docs/development.md).
DNS ?= false
# The cp operator's required --base-domain, which every CCLInfraPlane's domain is derived
# under: <name>.<namespace>.<BASE_DOMAIN>. With DNS=true, the external-dns zone must
# cover it.
BASE_DOMAIN ?= example.com
# The chart catalog the operator points its generated Applications at. Override
# CATALOG_REVISION to deploy from a branch that is not yet merged; Argo CD's
# repo-server clones the repository itself, so the revision must be pushed.
CATALOG_REPO_URL ?= https://github.com/NVIDIA/ccluster
CATALOG_REVISION ?= main
# Argo CD clones the catalog anonymously unless told otherwise, and GitHub answers a
# private repository with "authentication required: Repository not found". Set
# GITHUB_TOKEN (a token with read access to CATALOG_REPO_URL; in GitHub Actions the
# built-in one) and deploy-cp registers the catalog as an Argo CD repository with that
# credential, through the argo-cd chart's configs.repositories. Empty means anonymous.
#
# The token is expanded by the recipe's shell ($$GITHUB_TOKEN, exported below) rather than
# by make, so that make's echo of the helm command shows the variable name, not the value.
#
# Unset, it defaults to `gh auth token` when the GitHub CLI is installed and logged in, so
# a local `make deploy-cp` picks up the developer's own credential without being told. The
# assignment is guarded on `origin` so an explicit GITHUB_TOKEN=... on the command line or
# in the environment still wins, and simply-expanded so gh runs at most once per make.
ifeq ($(origin GITHUB_TOKEN),undefined)
GITHUB_TOKEN := $(shell command -v gh >/dev/null 2>&1 && gh auth token 2>/dev/null)
endif
export GITHUB_TOKEN
CATALOG_CREDENTIALS = $(if $(GITHUB_TOKEN),\
	--set argo-cd.configs.repositories.catalog.url=$(CATALOG_REPO_URL) \
	--set argo-cd.configs.repositories.catalog.username=git \
	--set-string "argo-cd.configs.repositories.catalog.password=$$GITHUB_TOKEN",)
# The kubeconfig context each operator is deployed to. Empty means "whatever the
# current context is", which is what deploy-cp / deploy-ip on their own have always
# done. `make deploy` sets them to the two development clusters.
#
# This is the supported way to aim a target at a specific cluster: KUBECTL is quoted in
# the recipes, so KUBECTL="kubectl --context foo" does not work.
KUBE_CONTEXT_CP ?=
KUBE_CONTEXT_IP ?=
HELM_CONTEXT_CP = $(if $(KUBE_CONTEXT_CP),--kube-context $(KUBE_CONTEXT_CP),)
HELM_CONTEXT_IP = $(if $(KUBE_CONTEXT_IP),--kube-context $(KUBE_CONTEXT_IP),)

# Extra flags passed through to `helm upgrade --install`.
HELM_ARGS ?=

.PHONY: helm
helm: ## Verify that the helm binary is available.
	@command -v "$(HELM)" >/dev/null 2>&1 || { \
		echo "helm not found. Install it from https://helm.sh/docs/intro/install/ or set HELM=<path>." >&2; \
		exit 1; \
	}

UV ?= uv

.PHONY: uv
uv: ## Verify that the uv binary is available.
	@command -v "$(UV)" >/dev/null 2>&1 || { \
		echo "uv not found. Install it from https://docs.astral.sh/uv/getting-started/installation/ or set UV=<path>." >&2; \
		exit 1; \
	}

.PHONY: helm-generate
helm-generate: kubebuilder build-installer-cp build-installer-ip ## Regenerate both operator charts from the kustomize overlays.
	rm -rf $(CHART_IP)/templates $(CHART_CP)/templates
	"$(KUBEBUILDER)" edit --plugins=helm/v2-alpha --manifests=dist/install-ip.yaml --output-dir=charts/ip
	"$(KUBEBUILDER)" edit --plugins=helm/v2-alpha --manifests=dist/install-cp.yaml --output-dir=charts/cp

.PHONY: helm-adopt-crds-cp
helm-adopt-crds-cp: ## Hand CRDs installed by `make install` (or the old kustomize deploy-cp) over to the Helm release. Non-destructive; existing CRs are preserved.
	@names="$$("$(KUBECTL)" get crd -o name -l 'app.kubernetes.io/managed-by!=Helm' 2>/dev/null \
		| grep -E 'ccluster\.nvidia\.com$$' || true)"; \
	if [ -z "$$names" ]; then echo "No unowned ccluster CRDs found; nothing to adopt."; exit 0; fi; \
	echo "Adopting into release $(HELM_RELEASE_CP)/$(HELM_NAMESPACE_CP):"; echo "$$names"; \
	"$(KUBECTL)" label $$names app.kubernetes.io/managed-by=Helm --overwrite; \
	"$(KUBECTL)" annotate $$names \
		meta.helm.sh/release-name=$(HELM_RELEASE_CP) \
		meta.helm.sh/release-namespace=$(HELM_NAMESPACE_CP) --overwrite

# `helm dependency build` resolves a dependency's repository through the local repo
# list, so the repo must be registered first or it fails with "no repository
# definition" on a clean machine (notably CI). `dependency update` resolves the URL
# directly and does not need this, but it also rewrites Chart.lock, which would defeat
# the point of pinning. The aliases are namespaced so they cannot clobber an existing
# `argo` or `external-dns` entry in a developer's Helm config; matching is by URL, not by
# name.
ARGOCD_REPO_NAME ?= ccluster-argo
ARGOCD_REPO_URL ?= https://argoproj.github.io/argo-helm
EXTERNAL_DNS_REPO_NAME ?= ccluster-external-dns
EXTERNAL_DNS_REPO_URL ?= https://kubernetes-sigs.github.io/external-dns/

.PHONY: helm-repos-cp
helm-repos-cp: helm ## Register the chart repositories the cp chart's dependencies come from.
	"$(HELM)" repo add $(ARGOCD_REPO_NAME) $(ARGOCD_REPO_URL) --force-update
	"$(HELM)" repo add $(EXTERNAL_DNS_REPO_NAME) $(EXTERNAL_DNS_REPO_URL) --force-update

.PHONY: helm-deps-cp
helm-deps-cp: helm-repos-cp ## Vendor the cp chart's dependencies (Argo CD, external-dns) exactly as pinned in Chart.lock.
	"$(HELM)" dependency build $(CHART_CP)

.PHONY: helm-deps-update-cp
helm-deps-update-cp: helm-repos-cp ## Re-resolve the cp chart's dependencies and refresh Chart.lock. Use when bumping a dependency's version.
	"$(HELM)" dependency update $(CHART_CP)

.PHONY: helm-lint
helm-lint: helm helm-deps-cp ## Lint both operator charts.
	"$(HELM)" lint $(CHART_CP) $(CHART_IP)

.PHONY: helm-lint-catalogs
helm-lint-catalogs: helm yq ## Lint and render every catalog chart, with its ci/*-values.yaml if any. Needs network for chart dependencies.
	HELM="$(HELM)" YQ="$(YQ)" hack/helm-lint-catalogs.sh

##@ Release

# Two kinds of build, one set of targets.
#
# A main-branch build is keyed by the commit: images are :<sha>, and the charts - which
# need a semver version - are published as <next>-dev.<sha>, where <next> is the VERSION
# file with the patch bumped (0.0.2-dev.28ecd0dbadf6 after v0.0.1), with the image tag
# and --catalog-revision inside them set to the same sha. The Python package is published
# as <next>.dev<N>, N being the commit count (0.0.2.dev57): PEP 440 has no place for a
# hash and PyPI refuses local versions. That is what .github/workflows/main.yml pushes for
# every merge. The pre-release form keeps dev builds ordered between the release they
# follow and the one they precede; `helm search --devel` and `pip install --pre` (or an
# exact pin) are needed to see them.
#
# A release (VERSION=X.Y.Z, set by .github/workflows/release.yml from the GitHub Release's
# tag) never rebuilds an image: docker-retag-* adds the version tag to the :<sha> manifest
# already in the registry. Helm has no server-side retag and the version lives inside the
# tarball, so the charts are re-packaged from the identical tagged commit with the release
# version and pointed at the :X.Y.Z image. The Python package is rebuilt as X.Y.Z.
#
# Nothing committed in the charts or python/ is bumped: they carry `version: 0.0.0-dev` and
# 0.0.0.dev0 forever, and everything below stamps a copy under dist/ from VERSION, or
# <next>-dev.<sha> / <next>.dev<N> when it is unset. The only committed version is the
# VERSION file, which check-version compares against the tag.
CHART_VERSION   = $(or $(VERSION),$(NEXT_VERSION)-dev.$(IMG_TAG))
CHART_IMAGE_TAG = $(or $(VERSION),$(IMG_TAG))
CATALOG_REV     = $(if $(VERSION),v$(VERSION),$(IMG_TAG))
# The CLI is versioned as the charts are; its tarballs are named with this bare version,
# and the binary reports it with a leading v (CLI_LDFLAGS).
CLI_VERSION     = $(CHART_VERSION)
# PEP 440 spelling; uv normalises pre-releases (0.1.0-rc.1 -> 0.1.0rc1).
PY_VERSION      = $(or $(VERSION),$(NEXT_VERSION).dev$(DEV_NUMBER))
DIST_CHARTS ?= dist/charts
DIST_PYTHON ?= dist/python
DIST_CLI ?= dist/cli
# Platforms cli-package builds the CLI for: GOOS/GOARCH pairs.
CLI_PLATFORMS ?= linux/amd64 linux/arm64 darwin/amd64 darwin/arm64

.PHONY: helm-package
helm-package: helm-package-cp helm-package-ip ## Package both charts into $(DIST_CHARTS) as $(CHART_VERSION).

# Packaging works on a copy so charts/{cp,ip}/chart stay byte-identical to what
# `make helm-generate` emits; CI fails on any diff there. The stamped values are the
# image tag (the template is `repository:tag`, with no appVersion fallback) and, for cp,
# --catalog-revision, so a chart always pins the catalog to the commit or tag it came from.
# The copy also gets the project LICENSE and THIRD_PARTY_NOTICES.md, so every packaged
# chart carries its license payload; the cp chart's notices cover its bundled argo-cd.
CHART_LICENSE_FILES ?= LICENSE THIRD_PARTY_NOTICES.md

.PHONY: helm-package-cp
helm-package-cp: helm helm-deps-cp yq ## Package the cp chart as $(DIST_CHARTS)/ccluster-cp-$(CHART_VERSION).tgz.
	rm -rf $(DIST_CHARTS)/ccluster-cp && mkdir -p $(DIST_CHARTS)
	cp -R $(CHART_CP) $(DIST_CHARTS)/ccluster-cp
	cp $(CHART_LICENSE_FILES) $(DIST_CHARTS)/ccluster-cp/
	"$(YQ)" -i '.manager.image.repository = "$(REGISTRY)/ccluster-cp-operator" | .manager.image.tag = "$(CHART_IMAGE_TAG)"' \
		$(DIST_CHARTS)/ccluster-cp/values.yaml
	"$(YQ)" -i '(.manager.args[] | select(. == "--catalog-revision=*")) = "--catalog-revision=$(CATALOG_REV)"' \
		$(DIST_CHARTS)/ccluster-cp/values.yaml
	"$(HELM)" package $(DIST_CHARTS)/ccluster-cp --version $(CHART_VERSION) --app-version $(CHART_IMAGE_TAG) -d $(DIST_CHARTS)

.PHONY: helm-package-ip
helm-package-ip: helm yq ## Package the ip chart as $(DIST_CHARTS)/ccluster-ip-$(CHART_VERSION).tgz.
	rm -rf $(DIST_CHARTS)/ccluster-ip && mkdir -p $(DIST_CHARTS)
	cp -R $(CHART_IP) $(DIST_CHARTS)/ccluster-ip
	cp $(CHART_LICENSE_FILES) $(DIST_CHARTS)/ccluster-ip/
	"$(YQ)" -i '.manager.image.repository = "$(REGISTRY)/ccluster-ip-operator" | .manager.image.tag = "$(CHART_IMAGE_TAG)"' \
		$(DIST_CHARTS)/ccluster-ip/values.yaml
	"$(HELM)" package $(DIST_CHARTS)/ccluster-ip --version $(CHART_VERSION) --app-version $(CHART_IMAGE_TAG) -d $(DIST_CHARTS)

.PHONY: helm-push
helm-push: helm-push-cp helm-push-ip ## Push both packaged charts to oci://$(REGISTRY)/charts.

.PHONY: helm-push-cp
helm-push-cp: helm ## Push the packaged cp chart to oci://$(REGISTRY)/charts.
	"$(HELM)" push $(DIST_CHARTS)/ccluster-cp-$(CHART_VERSION).tgz oci://$(REGISTRY)/charts

.PHONY: helm-push-ip
helm-push-ip: helm ## Push the packaged ip chart to oci://$(REGISTRY)/charts.
	"$(HELM)" push $(DIST_CHARTS)/ccluster-ip-$(CHART_VERSION).tgz oci://$(REGISTRY)/charts

# One tarball per platform, ccluster_<version>_<os>_<arch>.tar.gz, holding the static
# ccluster binary, the project LICENSE and THIRD_PARTY_NOTICES.md, and under licenses/go
# the license and NOTICE files of every linked Go module and the Go standard library,
# collected as the Dockerfile does for the images. The release workflow attaches them to
# the GitHub Release; `go install` is the other way to get the CLI.
.PHONY: cli-package
cli-package: go-licenses ## Cross-compile the ccluster CLI into $(DIST_CLI), one tarball per platform in CLI_PLATFORMS, as $(CLI_VERSION).
	rm -rf $(DIST_CLI) && mkdir -p $(DIST_CLI)
	for platform in $(CLI_PLATFORMS); do \
		os="$${platform%/*}"; arch="$${platform#*/}"; \
		name="ccluster_$(CLI_VERSION)_$${os}_$${arch}"; dir="$(DIST_CLI)/$$name"; \
		mkdir -p "$$dir"; \
		CGO_ENABLED=0 GOOS="$$os" GOARCH="$$arch" \
			go build -trimpath -ldflags "-s -w $(CLI_LDFLAGS)" -o "$$dir/ccluster" ./cmd/ccluster; \
		cp $(CHART_LICENSE_FILES) "$$dir/"; \
		CGO_ENABLED=0 GOOS="$$os" GOARCH="$$arch" "$(GO_LICENSES)" save ./cmd/ccluster \
			--save_path "$$dir/licenses/go" --ignore github.com/NVIDIA/ccluster; \
		mkdir -p "$$dir/licenses/go/stdlib" && cp "$$(go env GOROOT)/LICENSE" "$$dir/licenses/go/stdlib/"; \
		tar -C $(DIST_CLI) -czf "$(DIST_CLI)/$$name.tar.gz" "$$name"; \
		rm -rf "$$dir"; \
	done

# `uv version` edits python/pyproject.toml in place; that is intended in CI and is undone
# locally with `git checkout python/pyproject.toml`. Publishing is left to the workflows
# (PyPI trusted publishing), which need no credentials in the Makefile. A shallow clone
# counts only the commits it has, so a dev build refuses one rather than reuse a number.
.PHONY: python-build
python-build: uv ## Build the ccluster-py wheel and sdist into $(DIST_PYTHON) as $(PY_VERSION).
	@if [ -z "$(VERSION)" ] && [ "$$(git rev-parse --is-shallow-repository 2>/dev/null)" = true ]; then \
		echo "python-build: a dev build needs the full history to number it; fetch it or set VERSION" >&2; exit 1; \
	fi
	rm -rf $(DIST_PYTHON)
	cd python && "$(UV)" version --no-sync $(PY_VERSION) && "$(UV)" build --out-dir ../$(DIST_PYTHON)

##@ Python

# python/ is a Pydantic rendering of the CRDs' spec and status and of the catalog's typed
# values, generated in two steps. hack/pyschema writes one JSON Schema per CRD kind and
# version (spec at the root, status in definitions) and one per chart (reflected from the
# chart's Go Values), plus each API version package's __init__.py: the Kind enum and one
# concrete CustomResource per kind over the hand-written template in <version>/base.py.
# datamodel-code-generator then turns each schema into Python: <version>/<kind>.py for a
# CRD, <chart path>/__init__.py for a chart, the latter with a CHART constant appended.
# Catalog directories carry no __init__.py (namespace packages). Generated files are
# rebuilt on each run; see hack/pyschema/main.go.
PYSCHEMA_DIR ?= dist/pyschema
PYTHON_PKG ?= python/src/ccluster
CRD_DIR ?= config/crd/bases

.PHONY: python-generate
python-generate: manifests uv ## Regenerate python/ from the CRDs and the catalog Go types.
	go run ./hack/pyschema --out "$(PYSCHEMA_DIR)" --python "$(PYTHON_PKG)" --crds "$(CRD_DIR)"
	@find "$(PYSCHEMA_DIR)" -name '*.json' | sort | while read -r schema; do \
		rel="$${schema#$(PYSCHEMA_DIR)/}"; pkg="$${rel%.json}"; \
		case "$$pkg" in \
			catalogs/*) out="$(PYTHON_PKG)/$$pkg/__init__.py";; \
			*) out="$(PYTHON_PKG)/$$pkg.py";; \
		esac; \
		echo "$$schema -> $$out"; \
		"$(UV)" tool run --from "datamodel-code-generator==$(DATAMODEL_CODEGEN_VERSION)" \
			datamodel-codegen \
			--input "$$schema" --input-file-type jsonschema --output "$$out" \
			--output-model-type pydantic_v2.BaseModel --target-python-version 3.10 \
			--use-annotated --field-constraints --snake-case-field \
			--allow-population-by-field-name --use-title-as-name \
			--use-schema-description --use-field-description --use-default \
			--collapse-root-models --reuse-model --use-double-quotes --disable-timestamp \
			--formatters black isort --custom-file-header-path hack/boilerplate.py.txt \
			|| exit 1; \
		case "$$pkg" in catalogs/*) printf '\n\n# CHART is the chart directory, relative to the root of the catalog repository.\nCHART = "%s"\n' "$$pkg" >> "$$out";; esac; \
	done

.PHONY: python-test
python-test: uv ## Run the python/ package's tests.
	cd python && "$(UV)" sync --quiet && "$(UV)" run pytest

##@ Dependencies

## Location to install dependencies to
LOCALBIN ?= $(shell pwd)/bin
$(LOCALBIN):
	mkdir -p "$(LOCALBIN)"

## Tool Binaries
# Each tool's path is the binary itself, in a directory named <tool>@<version>-<toolchain>
# (kubebuilder, downloaded rather than built, has no toolchain). Bumping either names a
# file that does not exist yet, so Make builds it; generators built with another Go can
# emit different code. The binary keeps its own name because the k8s code generators
# write it into the "Code generated by" header.
KUBECTL ?= kubectl
KIND ?= $(LOCALBIN)/kind@$(KIND_VERSION)-$(GOTOOLCHAIN)/kind
KUSTOMIZE ?= $(LOCALBIN)/kustomize@$(KUSTOMIZE_VERSION)-$(GOTOOLCHAIN)/kustomize
YQ ?= $(LOCALBIN)/yq@$(YQ_VERSION)-$(GOTOOLCHAIN)/yq
CONTROLLER_GEN ?= $(LOCALBIN)/controller-gen@$(CONTROLLER_TOOLS_VERSION)-$(GOTOOLCHAIN)/controller-gen
APPLYCONFIGURATION_GEN ?= $(LOCALBIN)/applyconfiguration-gen@$(CODE_GENERATOR_VERSION)-$(GOTOOLCHAIN)/applyconfiguration-gen
CLIENT_GEN ?= $(LOCALBIN)/client-gen@$(CODE_GENERATOR_VERSION)-$(GOTOOLCHAIN)/client-gen
LISTER_GEN ?= $(LOCALBIN)/lister-gen@$(CODE_GENERATOR_VERSION)-$(GOTOOLCHAIN)/lister-gen
INFORMER_GEN ?= $(LOCALBIN)/informer-gen@$(CODE_GENERATOR_VERSION)-$(GOTOOLCHAIN)/informer-gen
ENVTEST ?= $(LOCALBIN)/setup-envtest@$(ENVTEST_VERSION)-$(GOTOOLCHAIN)/setup-envtest
GO_LICENSES ?= $(LOCALBIN)/go-licenses@$(GO_LICENSES_VERSION)-$(GOTOOLCHAIN)/go-licenses
GOLANGCI_LINT = $(LOCALBIN)/golangci-lint@$(GOLANGCI_LINT_VERSION)-$(GOTOOLCHAIN)/golangci-lint
# kubebuilder is pinned and downloaded into bin/ like the other tools, because the
# helm/v2-alpha plugin's output changes between releases (v4.14.0 started emitting the
# imagePullSecrets block and renamed the values keys) and the chart drift check in CI
# is only meaningful when every machine regenerates with the same version. The CLI is
# not `go install`able into a usable state - the release binaries carry their version
# via ldflags, which kubebuilder writes into PROJECT - so this fetches the GitHub
# release asset for the host platform.
KUBEBUILDER ?= $(LOCALBIN)/kubebuilder@$(KUBEBUILDER_VERSION)/kubebuilder

## Tool Versions
# gomodver prints the version go.mod resolves for module $(1). It is defined before the
# versions that call it, because tool paths are expanded when Make reads their rules.
# Versions in tool paths are therefore set once with := inside ifndef, so go list runs
# once per make invocation rather than on every expansion, and a command-line or
# environment value still overrides them as ?= would.
define gomodver
$(shell go list -m -f '{{if .Replace}}{{.Replace.Version}}{{else}}{{.Version}}{{end}}' $(1) 2>/dev/null)
endef

KUSTOMIZE_VERSION ?= v5.8.1
YQ_VERSION ?= v4.53.6
CONTROLLER_TOOLS_VERSION ?= v0.20.1

#CODE_GENERATOR_VERSION is the k8s.io/code-generator release matching the client-go the generated client is built against
ifndef CODE_GENERATOR_VERSION
CODE_GENERATOR_VERSION := $(shell v='$(call gomodver,k8s.io/client-go)'; \
  [ -n "$$v" ] || { echo "Set CODE_GENERATOR_VERSION manually (client-go replace has no tag)" >&2; exit 1; }; \
  printf '%s\n' "$$v")
endif

#ENVTEST_VERSION is the version of controller-runtime release branch to fetch the envtest setup script (i.e. release-0.20)
ifndef ENVTEST_VERSION
ENVTEST_VERSION := $(shell v='$(call gomodver,sigs.k8s.io/controller-runtime)'; \
  [ -n "$$v" ] || { echo "Set ENVTEST_VERSION manually (controller-runtime replace has no tag)" >&2; exit 1; }; \
  printf '%s\n' "$$v" | sed -E 's/^v?([0-9]+)\.([0-9]+).*/release-\1.\2/')
endif

#ENVTEST_K8S_VERSION is the version of Kubernetes to use for setting up ENVTEST binaries (i.e. 1.31)
ENVTEST_K8S_VERSION ?= $(shell v='$(call gomodver,k8s.io/api)'; \
  [ -n "$$v" ] || { echo "Set ENVTEST_K8S_VERSION manually (k8s.io/api replace has no tag)" >&2; exit 1; }; \
  printf '%s\n' "$$v" | sed -E 's/^v?[0-9]+\.([0-9]+).*/1.\1/')

# .custom-gcl.yml holds the golangci-lint version: `golangci-lint custom` reads it when
# it rebuilds the binary with the plugins, so the install below has to agree with it.
GOLANGCI_LINT_VERSION ?= $(shell v="$$(sed -n 's/^version: *//p' .custom-gcl.yml)"; \
  [ -n "$$v" ] || { echo "Set GOLANGCI_LINT_VERSION manually (.custom-gcl.yml has no version line)" >&2; exit 1; }; \
  printf '%s\n' "$$v")
KUBEBUILDER_VERSION ?= v4.15.0
# The Dockerfile's go-licenses, so the CLI tarballs and the images collect licenses alike.
GO_LICENSES_VERSION ?= $(shell sed -n 's/^ARG GO_LICENSES_VERSION=//p' Dockerfile)
# kind and the node image it creates clusters from are pinned together, so local runs and
# CI get the same Kubernetes. Take the image, with its digest, from the kind release's
# notes; bump both in one change. Dependabot updates neither.
KIND_VERSION ?= v0.33.0
KIND_NODE_IMAGE ?= kindest/node:v1.37.0@sha256:a1ed56cfb0e7b93589bdf97c8cd566405a265939e3620fc4f5de89adff580ae5
DATAMODEL_CODEGEN_VERSION ?= 0.76.1
.PHONY: kind
kind: $(KIND) ## Build kind locally if necessary.
$(KIND): | $(LOCALBIN)
	$(call go-install-tool,$(KIND),sigs.k8s.io/kind,$(KIND_VERSION))

.PHONY: kustomize
kustomize: $(KUSTOMIZE) ## Download kustomize locally if necessary.
$(KUSTOMIZE): | $(LOCALBIN)
	$(call go-install-tool,$(KUSTOMIZE),sigs.k8s.io/kustomize/kustomize/v5,$(KUSTOMIZE_VERSION))

.PHONY: yq
yq: $(YQ) ## Download yq locally if necessary.
$(YQ): | $(LOCALBIN)
	$(call go-install-tool,$(YQ),github.com/mikefarah/yq/v4,$(YQ_VERSION))

.PHONY: controller-gen
controller-gen: $(CONTROLLER_GEN) ## Download controller-gen locally if necessary.
$(CONTROLLER_GEN): | $(LOCALBIN)
	$(call go-install-tool,$(CONTROLLER_GEN),sigs.k8s.io/controller-tools/cmd/controller-gen,$(CONTROLLER_TOOLS_VERSION))

.PHONY: applyconfiguration-gen
applyconfiguration-gen: $(APPLYCONFIGURATION_GEN) ## Download applyconfiguration-gen locally if necessary.
$(APPLYCONFIGURATION_GEN): | $(LOCALBIN)
	$(call go-install-tool,$(APPLYCONFIGURATION_GEN),k8s.io/code-generator/cmd/applyconfiguration-gen,$(CODE_GENERATOR_VERSION))

.PHONY: client-gen
client-gen: $(CLIENT_GEN) ## Download client-gen locally if necessary.
$(CLIENT_GEN): | $(LOCALBIN)
	$(call go-install-tool,$(CLIENT_GEN),k8s.io/code-generator/cmd/client-gen,$(CODE_GENERATOR_VERSION))

.PHONY: lister-gen
lister-gen: $(LISTER_GEN) ## Download lister-gen locally if necessary.
$(LISTER_GEN): | $(LOCALBIN)
	$(call go-install-tool,$(LISTER_GEN),k8s.io/code-generator/cmd/lister-gen,$(CODE_GENERATOR_VERSION))

.PHONY: informer-gen
informer-gen: $(INFORMER_GEN) ## Download informer-gen locally if necessary.
$(INFORMER_GEN): | $(LOCALBIN)
	$(call go-install-tool,$(INFORMER_GEN),k8s.io/code-generator/cmd/informer-gen,$(CODE_GENERATOR_VERSION))

.PHONY: setup-envtest
setup-envtest: envtest ## Download the binaries required for ENVTEST in the local bin directory.
	@echo "Setting up envtest binaries for Kubernetes version $(ENVTEST_K8S_VERSION)..."
	@"$(ENVTEST)" use $(ENVTEST_K8S_VERSION) --bin-dir "$(LOCALBIN)" -p path || { \
		echo "Error: Failed to set up envtest binaries for version $(ENVTEST_K8S_VERSION)."; \
		exit 1; \
	}

.PHONY: envtest
envtest: $(ENVTEST) ## Download setup-envtest locally if necessary.
$(ENVTEST): | $(LOCALBIN)
	$(call go-install-tool,$(ENVTEST),sigs.k8s.io/controller-runtime/tools/setup-envtest,$(ENVTEST_VERSION))

.PHONY: go-licenses
go-licenses: $(GO_LICENSES) ## Download go-licenses locally if necessary.
$(GO_LICENSES): | $(LOCALBIN)
	$(call go-install-tool,$(GO_LICENSES),github.com/google/go-licenses/v2,$(GO_LICENSES_VERSION))

# The plain install is rebuilt in place with the plugins .custom-gcl.yml lists, so a
# change to that file rebuilds it, and a failed plugin build leaves no binary behind.
.PHONY: golangci-lint
golangci-lint: $(GOLANGCI_LINT) ## Download golangci-lint locally if necessary.
$(GOLANGCI_LINT): .custom-gcl.yml | $(LOCALBIN)
	$(call go-install-tool,$(GOLANGCI_LINT),github.com/golangci/golangci-lint/v2/cmd/golangci-lint,$(GOLANGCI_LINT_VERSION))
	@echo "Building custom golangci-lint with plugins..."
	"$(GOLANGCI_LINT)" custom --destination "$(dir $(GOLANGCI_LINT))" --name golangci-lint-custom || \
		{ rm -f "$(GOLANGCI_LINT)"; exit 1; }
	mv -f "$(dir $(GOLANGCI_LINT))golangci-lint-custom" "$(GOLANGCI_LINT)"

.PHONY: kubebuilder
kubebuilder: $(KUBEBUILDER) ## Download kubebuilder locally if necessary.
$(KUBEBUILDER): | $(LOCALBIN)
	@echo "Downloading kubebuilder $(KUBEBUILDER_VERSION)"
	@mkdir -p "$(dir $(KUBEBUILDER))"
	@curl -fsSL -o "$(KUBEBUILDER).tmp" \
		"https://github.com/kubernetes-sigs/kubebuilder/releases/download/$(KUBEBUILDER_VERSION)/kubebuilder_$$(go env GOOS)_$$(go env GOARCH)"
	@chmod +x "$(KUBEBUILDER).tmp" && mv -f "$(KUBEBUILDER).tmp" "$(KUBEBUILDER)"

# go-install-tool will 'go install' a package into the directory of the binary path given,
# whose file name must be the one go install gives the package's binary.
# $1 - target path with name of binary
# $2 - package url which can be installed
# $3 - specific version of package
define go-install-tool
@echo "Downloading $(2)@$(3)"
@GOBIN="$(dir $(1))" go install "$(2)@$(3)"
endef

