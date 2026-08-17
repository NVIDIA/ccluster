# Image URLs to use for all building/pushing image targets. The two operators
# ship as separate images, built from the same Dockerfile via --build-arg CMD.
IMG_CP ?= ccluster-cp-operator:dev
IMG_IP ?= ccluster-ip-operator:dev

# Get the currently used golang install path (in GOPATH/bin, unless GOBIN is set)
ifeq (,$(shell go env GOBIN))
GOBIN=$(shell go env GOPATH)/bin
else
GOBIN=$(shell go env GOBIN)
endif

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
all: build

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
manifests: controller-gen ## Generate WebhookConfiguration, ClusterRole and CustomResourceDefinition objects.
	# CRDs once, over every API group.
	"$(CONTROLLER_GEN)" crd webhook paths="./api/..." output:crd:artifacts:config=config/crd/bases
	# RBAC once per operator, so each ClusterRole holds only that operator's rules.
	# The role name is the same in both; the overlay namePrefix disambiguates them
	# into ccluster-cp-manager-role and ccluster-ip-manager-role, which is what lets
	# config/{cp,ip}/rbac/role_binding.yaml be identical.
	"$(CONTROLLER_GEN)" rbac:roleName=manager-role paths="./internal/controller/cp/..." \
		output:rbac:artifacts:config=config/cp/rbac
	# The ip pass is disabled until ccluster-ip-operator has its first controller:
	# controller-gen writes no file at all when a package has no +kubebuilder:rbac
	# markers, which would delete nothing but leave config/ip/rbac/role.yaml stale.
	# That file is hand-written (empty rules) for now — see the note in it.
	#"$(CONTROLLER_GEN)" rbac:roleName=manager-role paths="./internal/controller/ip/..." \
	#	output:rbac:artifacts:config=config/ip/rbac

.PHONY: generate
generate: controller-gen ## Generate code containing DeepCopy, DeepCopyInto, and DeepCopyObject method implementations.
	"$(CONTROLLER_GEN)" object:headerFile="hack/boilerplate.go.txt" paths="./..."

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
KIND_CLUSTER ?= ccluster-test-e2e

.PHONY: setup-test-e2e
setup-test-e2e: ## Set up a Kind cluster for e2e tests if it does not exist
	@command -v $(KIND) >/dev/null 2>&1 || { \
		echo "Kind is not installed. Please install Kind manually."; \
		exit 1; \
	}
	@case "$$($(KIND) get clusters)" in \
		*"$(KIND_CLUSTER)"*) \
			echo "Kind cluster '$(KIND_CLUSTER)' already exists. Skipping creation." ;; \
		*) \
			echo "Creating Kind cluster '$(KIND_CLUSTER)'..."; \
			$(KIND) create cluster --name $(KIND_CLUSTER) ;; \
	esac

.PHONY: test-e2e
test-e2e: setup-test-e2e manifests generate fmt vet ## Run the e2e tests. Expected an isolated environment using Kind.
	KIND=$(KIND) KIND_CLUSTER=$(KIND_CLUSTER) go test -tags=e2e ./test/e2e/ -v -ginkgo.v
	$(MAKE) cleanup-test-e2e

.PHONY: test-e2e-argocd
test-e2e-argocd: setup-test-e2e docker-build-cp ## Install the cp chart WITH Argo CD into a Kind cluster and verify it comes up. Pulls Argo CD images from the internet.
	$(KIND) load docker-image $(IMG_CP) --name $(KIND_CLUSTER)
	$(MAKE) deploy-cp HELM_TIMEOUT=10m
	"$(KUBECTL)" get crd applications.argoproj.io applicationsets.argoproj.io
	"$(KUBECTL)" -n $(HELM_NAMESPACE_CP) rollout status deploy/ccluster-cp-controller-manager --timeout=2m
	$(MAKE) undeploy-cp
	$(MAKE) cleanup-test-e2e

.PHONY: cleanup-test-e2e
cleanup-test-e2e: ## Tear down the Kind cluster used for e2e tests
	@$(KIND) delete cluster --name $(KIND_CLUSTER)

.PHONY: lint
lint: golangci-lint ## Run golangci-lint linter
	"$(GOLANGCI_LINT)" run

.PHONY: lint-fix
lint-fix: golangci-lint ## Run golangci-lint linter and perform fixes
	"$(GOLANGCI_LINT)" run --fix

.PHONY: lint-config
lint-config: golangci-lint ## Verify golangci-lint linter configuration
	"$(GOLANGCI_LINT)" config verify

##@ Build

.PHONY: build
build: build-cp build-ip ## Build both manager binaries.

.PHONY: build-cp
build-cp: manifests generate fmt vet ## Build the ccluster-cp-operator binary.
	go build -o bin/ccluster-cp-operator ./cmd/ccluster-cp-operator

.PHONY: build-ip
build-ip: manifests generate fmt vet ## Build the ccluster-ip-operator binary.
	go build -o bin/ccluster-ip-operator ./cmd/ccluster-ip-operator

.PHONY: run-cp
run-cp: manifests generate fmt vet ## Run ccluster-cp-operator from your host.
	go run ./cmd/ccluster-cp-operator

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
	$(CONTAINER_TOOL) build --build-arg CMD=ccluster-cp-operator -t ${IMG_CP} .

.PHONY: docker-build-ip
docker-build-ip: ## Build docker image with the ccluster-ip-operator manager.
	$(CONTAINER_TOOL) build --build-arg CMD=ccluster-ip-operator -t ${IMG_IP} .

.PHONY: docker-push
docker-push: docker-push-cp docker-push-ip ## Push both manager images.

.PHONY: docker-push-cp
docker-push-cp: ## Push the ccluster-cp-operator docker image.
	$(CONTAINER_TOOL) push ${IMG_CP}

.PHONY: docker-push-ip
docker-push-ip: ## Push the ccluster-ip-operator docker image.
	$(CONTAINER_TOOL) push ${IMG_IP}

# PLATFORMS defines the target platforms for the manager image be built to provide support to multiple
# architectures. (i.e. make docker-buildx IMG=myregistry/mypoperator:0.0.1). To use this option you need to:
# - be able to use docker buildx. More info: https://docs.docker.com/build/buildx/
# - have enabled BuildKit. More info: https://docs.docker.com/develop/develop-images/build_enhancements/
# - be able to push the image to your registry (i.e. if you do not set a valid value via IMG=<myregistry/image:<tag>> then the export will fail)
# To adequately provide solutions that are compatible with multiple platforms, you should consider using this option.
PLATFORMS ?= linux/arm64,linux/amd64,linux/s390x,linux/ppc64le
# CMD and IMG select which operator to cross-build; override both together, e.g.
#   make docker-buildx CMD=ccluster-ip-operator IMG=myregistry/ccluster-ip-operator:0.0.1
CMD ?= ccluster-cp-operator
IMG ?= $(IMG_CP)
.PHONY: docker-buildx
docker-buildx: ## Build and push docker image for the manager for cross-platform support
	# copy existing Dockerfile and insert --platform=${BUILDPLATFORM} into Dockerfile.cross, and preserve the original Dockerfile
	sed -e '1 s/\(^FROM\)/FROM --platform=\$$\{BUILDPLATFORM\}/; t' -e ' 1,// s//FROM --platform=\$$\{BUILDPLATFORM\}/' Dockerfile > Dockerfile.cross
	- $(CONTAINER_TOOL) buildx create --name ccluster-builder
	$(CONTAINER_TOOL) buildx use ccluster-builder
	- $(CONTAINER_TOOL) buildx build --push --platform=$(PLATFORMS) --build-arg CMD=$(CMD) --tag ${IMG} -f Dockerfile.cross .
	- $(CONTAINER_TOOL) buildx rm ccluster-builder
	rm Dockerfile.cross

.PHONY: build-installer
build-installer: build-installer-cp build-installer-ip ## Generate consolidated install YAML for both operators.

.PHONY: build-installer-cp
build-installer-cp: manifests generate kustomize ## Generate dist/install-cp.yaml (CRDs + cp deployment).
	mkdir -p dist
	cd config/cp && "$(KUSTOMIZE)" edit set image controller=${IMG_CP}
	"$(KUSTOMIZE)" build config/cp > dist/install-cp.yaml

.PHONY: build-installer-ip
build-installer-ip: manifests generate kustomize ## Generate dist/install-ip.yaml (ip deployment, no CRDs).
	mkdir -p dist
	cd config/ip && "$(KUSTOMIZE)" edit set image controller=${IMG_IP}
	"$(KUSTOMIZE)" build config/ip > dist/install-ip.yaml

##@ Deployment

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

# The chart's argo-cd dependency installs into the RELEASE namespace, not the
# `argocd` the operator defaults to, so deploy-cp overrides --argocd-namespace to
# match. manager.args is restated in full because `--set manager.args[1]=...` builds
# a sparse list and nulls index 0, silently dropping --leader-elect.
.PHONY: deploy-cp
deploy-cp: helm helm-deps-cp ## Deploy ccluster-cp-operator (and Argo CD) to the K8s cluster specified in ~/.kube/config.
	"$(HELM)" upgrade --install $(HELM_RELEASE_CP) $(CHART_CP) \
		--namespace $(HELM_NAMESPACE_CP) --create-namespace \
		--set manager.image.repository=$(word 1,$(subst :, ,$(IMG_CP))) \
		--set manager.image.tag=$(word 2,$(subst :, ,$(IMG_CP))) \
		--set argo-cd.enable=$(ARGOCD) \
		--set-json 'manager.args=["--leader-elect","--argocd-namespace=$(HELM_NAMESPACE_CP)"]' \
		--wait --timeout $(HELM_TIMEOUT) $(HELM_ARGS)

.PHONY: undeploy-cp
undeploy-cp: helm ## Undeploy ccluster-cp-operator. CRDs are retained (helm.sh/resource-policy: keep).
	"$(HELM)" uninstall $(HELM_RELEASE_CP) --namespace $(HELM_NAMESPACE_CP) --ignore-not-found

.PHONY: deploy-ip
deploy-ip: helm ## Deploy ccluster-ip-operator to the K8s cluster specified in ~/.kube/config.
	"$(HELM)" upgrade --install $(HELM_RELEASE_IP) $(CHART_IP) \
		--namespace $(HELM_NAMESPACE_IP) --create-namespace \
		--set manager.image.repository=$(word 1,$(subst :, ,$(IMG_IP))) \
		--set manager.image.tag=$(word 2,$(subst :, ,$(IMG_IP))) \
		--wait --timeout $(HELM_TIMEOUT) $(HELM_ARGS)

.PHONY: undeploy-ip
undeploy-ip: helm ## Undeploy ccluster-ip-operator.
	"$(HELM)" uninstall $(HELM_RELEASE_IP) --namespace $(HELM_NAMESPACE_IP) --ignore-not-found

##@ Helm

# The operator charts are GENERATED, not hand-written: `kubebuilder edit
# --plugins=helm/v2-alpha` parses the kustomize output in dist/install-{cp,ip}.yaml
# and emits charts/{cp,ip}/chart. config/ therefore stays the single source of truth
# for the manager Deployment, RBAC and CRDs, and controller-gen output can never
# drift from what gets deployed.
#
# The plugin preserves Chart.yaml and values.yaml and regenerates everything under
# templates/. Hand-edit only the former two — the Argo CD dependency and the pinned
# fullnameOverride live there for exactly that reason.
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
# Extra flags passed through to `helm upgrade --install`.
HELM_ARGS ?=

.PHONY: helm
helm: ## Verify that the helm binary is available.
	@command -v "$(HELM)" >/dev/null 2>&1 || { \
		echo "helm not found. Install it from https://helm.sh/docs/intro/install/ or set HELM=<path>." >&2; \
		exit 1; \
	}

KUBEBUILDER ?= kubebuilder

.PHONY: kubebuilder
kubebuilder: ## Verify that the kubebuilder CLI is available.
	@command -v "$(KUBEBUILDER)" >/dev/null 2>&1 || { \
		echo "kubebuilder not found. Install it from https://book.kubebuilder.io/quick-start or set KUBEBUILDER=<path>." >&2; \
		exit 1; \
	}

.PHONY: helm-generate
helm-generate: kubebuilder build-installer-cp build-installer-ip ## Regenerate both operator charts from the kustomize overlays.
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
# the point of pinning. The alias is namespaced so it cannot clobber an existing
# `argo` entry in a developer's Helm config; matching is by URL, not by name.
ARGOCD_REPO_NAME ?= ccluster-argo
ARGOCD_REPO_URL ?= https://argoproj.github.io/argo-helm

.PHONY: helm-repos-cp
helm-repos-cp: helm ## Register the chart repositories the cp chart's dependencies come from.
	"$(HELM)" repo add $(ARGOCD_REPO_NAME) $(ARGOCD_REPO_URL) --force-update

.PHONY: helm-deps-cp
helm-deps-cp: helm-repos-cp ## Vendor the cp chart's dependencies (Argo CD) exactly as pinned in Chart.lock.
	"$(HELM)" dependency build $(CHART_CP)

.PHONY: helm-deps-update-cp
helm-deps-update-cp: helm-repos-cp ## Re-resolve the cp chart's dependencies and refresh Chart.lock. Use when bumping the Argo CD version.
	"$(HELM)" dependency update $(CHART_CP)

.PHONY: helm-lint
helm-lint: helm helm-deps-cp ## Lint both operator charts.
	"$(HELM)" lint $(CHART_CP) $(CHART_IP)

##@ Dependencies

## Location to install dependencies to
LOCALBIN ?= $(shell pwd)/bin
$(LOCALBIN):
	mkdir -p "$(LOCALBIN)"

## Tool Binaries
KUBECTL ?= kubectl
KIND ?= kind
KUSTOMIZE ?= $(LOCALBIN)/kustomize
CONTROLLER_GEN ?= $(LOCALBIN)/controller-gen
ENVTEST ?= $(LOCALBIN)/setup-envtest
GOLANGCI_LINT = $(LOCALBIN)/golangci-lint

## Tool Versions
KUSTOMIZE_VERSION ?= v5.8.1
CONTROLLER_TOOLS_VERSION ?= v0.20.1

#ENVTEST_VERSION is the version of controller-runtime release branch to fetch the envtest setup script (i.e. release-0.20)
ENVTEST_VERSION ?= $(shell v='$(call gomodver,sigs.k8s.io/controller-runtime)'; \
  [ -n "$$v" ] || { echo "Set ENVTEST_VERSION manually (controller-runtime replace has no tag)" >&2; exit 1; }; \
  printf '%s\n' "$$v" | sed -E 's/^v?([0-9]+)\.([0-9]+).*/release-\1.\2/')

#ENVTEST_K8S_VERSION is the version of Kubernetes to use for setting up ENVTEST binaries (i.e. 1.31)
ENVTEST_K8S_VERSION ?= $(shell v='$(call gomodver,k8s.io/api)'; \
  [ -n "$$v" ] || { echo "Set ENVTEST_K8S_VERSION manually (k8s.io/api replace has no tag)" >&2; exit 1; }; \
  printf '%s\n' "$$v" | sed -E 's/^v?[0-9]+\.([0-9]+).*/1.\1/')

GOLANGCI_LINT_VERSION ?= v2.8.0
.PHONY: kustomize
kustomize: $(KUSTOMIZE) ## Download kustomize locally if necessary.
$(KUSTOMIZE): $(LOCALBIN)
	$(call go-install-tool,$(KUSTOMIZE),sigs.k8s.io/kustomize/kustomize/v5,$(KUSTOMIZE_VERSION))

.PHONY: controller-gen
controller-gen: $(CONTROLLER_GEN) ## Download controller-gen locally if necessary.
$(CONTROLLER_GEN): $(LOCALBIN)
	$(call go-install-tool,$(CONTROLLER_GEN),sigs.k8s.io/controller-tools/cmd/controller-gen,$(CONTROLLER_TOOLS_VERSION))

.PHONY: setup-envtest
setup-envtest: envtest ## Download the binaries required for ENVTEST in the local bin directory.
	@echo "Setting up envtest binaries for Kubernetes version $(ENVTEST_K8S_VERSION)..."
	@"$(ENVTEST)" use $(ENVTEST_K8S_VERSION) --bin-dir "$(LOCALBIN)" -p path || { \
		echo "Error: Failed to set up envtest binaries for version $(ENVTEST_K8S_VERSION)."; \
		exit 1; \
	}

.PHONY: envtest
envtest: $(ENVTEST) ## Download setup-envtest locally if necessary.
$(ENVTEST): $(LOCALBIN)
	$(call go-install-tool,$(ENVTEST),sigs.k8s.io/controller-runtime/tools/setup-envtest,$(ENVTEST_VERSION))

.PHONY: golangci-lint
golangci-lint: $(GOLANGCI_LINT) ## Download golangci-lint locally if necessary.
$(GOLANGCI_LINT): $(LOCALBIN)
	$(call go-install-tool,$(GOLANGCI_LINT),github.com/golangci/golangci-lint/v2/cmd/golangci-lint,$(GOLANGCI_LINT_VERSION))
	@test -f .custom-gcl.yml && { \
		echo "Building custom golangci-lint with plugins..." && \
		$(GOLANGCI_LINT) custom --destination $(LOCALBIN) --name golangci-lint-custom && \
		mv -f $(LOCALBIN)/golangci-lint-custom $(GOLANGCI_LINT); \
	} || true

# go-install-tool will 'go install' any package with custom target and name of binary, if it doesn't exist
# $1 - target path with name of binary
# $2 - package url which can be installed
# $3 - specific version of package
define go-install-tool
@[ -f "$(1)-$(3)" ] && [ "$$(readlink -- "$(1)" 2>/dev/null)" = "$(1)-$(3)" ] || { \
set -e; \
package=$(2)@$(3) ;\
echo "Downloading $${package}" ;\
rm -f "$(1)" ;\
GOBIN="$(LOCALBIN)" go install $${package} ;\
mv "$(LOCALBIN)/$$(basename "$(1)")" "$(1)-$(3)" ;\
} ;\
ln -sf "$$(realpath "$(1)-$(3)")" "$(1)"
endef

define gomodver
$(shell go list -m -f '{{if .Replace}}{{.Replace.Version}}{{else}}{{.Version}}{{end}}' $(1) 2>/dev/null)
endef

