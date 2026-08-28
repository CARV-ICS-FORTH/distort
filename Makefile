# Image URL to use all building/pushing image targets
IMG ?= distort:latest

# Isolated Vagrant/K3s test lab configuration.
VAGRANT_NODES ?= distort-master distort-worker-1 distort-worker-2
E2E_ARGS ?=
FINDING ?=
LOCAL_KUBECONFIG ?= $(shell pwd)/kubeconfig.yaml
TEST_ENV_IMAGE_REPOSITORY ?= localhost/distort
TEST_ENV_IMAGE_TAG ?= 0.5.0-dev
TEST_ENV_IMG = $(TEST_ENV_IMAGE_REPOSITORY):$(TEST_ENV_IMAGE_TAG)
TEST_ENV_BUILD_JOBS ?= 1
TEST_ENV_GO_BUILD_PROCS ?= 1
TEST_ENV_SKIP_IMAGE_BUILD ?= 0

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
	"$(CONTROLLER_GEN)" rbac:roleName=manager-role crd webhook paths="./..." output:crd:artifacts:config=config/crd/bases

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

.PHONY: test-regression
test-regression: manifests generate fmt vet setup-envtest ## Run quarantined tests for known review findings (optionally FINDING=F7).
	KUBEBUILDER_ASSETS="$(shell "$(ENVTEST)" use $(ENVTEST_K8S_VERSION) --bin-dir "$(LOCALBIN)" -p path)" DISTORT_RUN_KNOWN_FAILURES=1 DISTORT_FINDING="$(FINDING)" go test $$(go list ./... | grep -v /e2e) -count=1

.PHONY: test-suite
test-suite: test test-static lint ## Run the complete green host-side suite and compile E2E tests.
	go test -tags=e2e ./test/e2e -run '^$$'

.PHONY: test-static
test-static: ## Run repository contracts and validate Helm and Hugo artifacts.
	go test ./test/contracts -count=1
	helm lint ./deploy/charts/distort --set-string image.repository=registry.example.com/distort
	helm template distort ./deploy/charts/distort --namespace distort-system --set-string image.repository=registry.example.com/distort >/dev/null
	hugo --source docs --destination /tmp/distort-docs-test --minify

.PHONY: verify-modules
verify-modules: ## Verify go.mod and go.sum are tidy without modifying them.
	go mod tidy -diff

.PHONY: test-ci
test-ci: verify-modules test-suite test-race ## Run the complete host-side CI-equivalent validation.

.PHONY: test-race
test-race: setup-envtest ## Run host-side tests under the Go race detector.
	KUBEBUILDER_ASSETS="$(shell "$(ENVTEST)" use $(ENVTEST_K8S_VERSION) --bin-dir "$(LOCALBIN)" -p path)" go test -race $$(go list ./... | grep -v /e2e) -count=1

##@ E2E Testing on Vagrant

.PHONY: test-env-prereqs
test-env-prereqs: ## Verify tools required by the isolated Vagrant test lab.
	@command -v vagrant >/dev/null || { echo "ERROR: vagrant is required"; exit 1; }
	@command -v VBoxManage >/dev/null || { echo "ERROR: VirtualBox is required"; exit 1; }
	@command -v $(CONTAINER_TOOL) >/dev/null || { echo "ERROR: $(CONTAINER_TOOL) is required"; exit 1; }
	@command -v helm >/dev/null || { echo "ERROR: helm is required"; exit 1; }
	@command -v $(KUBECTL) >/dev/null || { echo "ERROR: $(KUBECTL) is required"; exit 1; }
	@vagrant --version >/dev/null || { echo "ERROR: vagrant is installed but unusable"; exit 1; }
	@VBoxManage --version >/dev/null || { echo "ERROR: VirtualBox is installed but unusable"; exit 1; }
	@if [ "$$(uname -s)" = Linux ] && [ ! -c /dev/vboxdrv ]; then \
		echo "ERROR: /dev/vboxdrv is unavailable; install/load the VirtualBox host kernel module"; exit 1; \
	fi
	@$(CONTAINER_TOOL) info >/dev/null || { echo "ERROR: $(CONTAINER_TOOL) daemon is unavailable to this user"; exit 1; }
	@helm version --short >/dev/null || { echo "ERROR: helm is installed but unusable"; exit 1; }
	@$(KUBECTL) version --client >/dev/null || { echo "ERROR: $(KUBECTL) is installed but unusable"; exit 1; }

.PHONY: test-env-up
test-env-up: test-env-prereqs ## Create/start the three-node isolated Vagrant K3s lab.
	@for node in $(VAGRANT_NODES); do \
		echo "Starting and provisioning $$node"; \
		(cd vagrant && vagrant up "$$node"); \
	done
	$(MAKE) get-kubeconfig
	KUBECTL="$(KUBECTL)" bash vagrant/verify-local-kubeconfig.sh "$(LOCAL_KUBECONFIG)"
	KUBECONFIG="$(LOCAL_KUBECONFIG)" $(KUBECTL) get nodes -o wide

.PHONY: test-env-create
test-env-create: test-env-prereqs ## Build with VMs stopped, create the low-memory lab, deploy, and run smoke tests.
	-cd vagrant && vagrant halt
	$(MAKE) test-env-image
	$(MAKE) test-env-up
	$(MAKE) test-env-deploy TEST_ENV_SKIP_IMAGE_BUILD=1
	$(MAKE) test-env-smoke

.PHONY: test-env-image
test-env-image: ## Build the lab image with constrained SPDK and Go parallelism.
	$(MAKE) docker-build IMG="$(TEST_ENV_IMG)" DOCKER_BUILD_ARGS="--build-arg SPDK_BUILD_JOBS=$(TEST_ENV_BUILD_JOBS) --build-arg GO_BUILD_PROCS=$(TEST_ENV_GO_BUILD_PROCS)"

.PHONY: get-kubeconfig
get-kubeconfig:
	cd vagrant && vagrant ssh distort-master -c "sudo cat /etc/rancher/k3s/k3s.yaml" | tr -d '\r' | sed -n '/^apiVersion: v1$$/,$$p' | sed "s/127.0.0.1/192.168.56.10/g" > "$(LOCAL_KUBECONFIG).tmp"
	@test -s "$(LOCAL_KUBECONFIG).tmp" || { echo "ERROR: fetched kubeconfig is empty"; exit 1; }
	@$(KUBECTL) --kubeconfig "$(LOCAL_KUBECONFIG).tmp" config view --minify >/dev/null || { rm -f "$(LOCAL_KUBECONFIG).tmp"; echo "ERROR: fetched kubeconfig is invalid; existing kubeconfig was preserved"; exit 1; }
	mv "$(LOCAL_KUBECONFIG).tmp" "$(LOCAL_KUBECONFIG)"
	chmod 600 "$(LOCAL_KUBECONFIG)"

.PHONY: test-env-guard
test-env-guard: get-kubeconfig ## Refuse lab mutations unless kubeconfig targets the isolated Vagrant cluster.
	KUBECTL="$(KUBECTL)" bash vagrant/verify-local-kubeconfig.sh "$(LOCAL_KUBECONFIG)"

.PHONY: test-env-reset
test-env-reset: test-env-guard ## Clean test resources and storage state without uninstalling DISTORT.
	-KUBECONFIG="$(LOCAL_KUBECONFIG)" $(KUBECTL) delete namespace distort-test --ignore-not-found --wait=true --timeout=120s
	-KUBECONFIG="$(LOCAL_KUBECONFIG)" $(KUBECTL) delete pod e2e-distort-pod --ignore-not-found --wait=true --timeout=90s
	-KUBECONFIG="$(LOCAL_KUBECONFIG)" $(KUBECTL) delete pvc e2e-distort-pvc --ignore-not-found --wait=true --timeout=90s
	-KUBECONFIG="$(LOCAL_KUBECONFIG)" $(KUBECTL) delete storageclass distort-csi-sc --ignore-not-found
	KUBECONFIG="$(LOCAL_KUBECONFIG)" $(KUBECTL) delete nvmepartitions --all --all-namespaces --ignore-not-found --wait=true --timeout=120s
	KUBECONFIG="$(LOCAL_KUBECONFIG)" $(KUBECTL) delete nvmedeviceclaims --all --all-namespaces --ignore-not-found --wait=true --timeout=120s
	@for node in $(VAGRANT_NODES); do \
		echo "Cleaning storage state on $$node"; \
		(cd vagrant && vagrant ssh "$$node" -c "sudo bash /vagrant/clean-node.sh"); \
	done
	-KUBECONFIG="$(LOCAL_KUBECONFIG)" $(KUBECTL) delete nvmedevices,rdmastoragenodes --all --ignore-not-found
	KUBECONFIG="$(LOCAL_KUBECONFIG)" $(KUBECTL) rollout restart -n distort-system daemonset/distort-agent
	KUBECONFIG="$(LOCAL_KUBECONFIG)" $(KUBECTL) rollout status -n distort-system daemonset/distort-agent --timeout=180s

.PHONY: test-env-deploy
test-env-deploy: test-env-guard manifests ## Build/load the image and Helm-upgrade the persistent Vagrant lab.
	@if [ "$(TEST_ENV_SKIP_IMAGE_BUILD)" != "1" ]; then \
		$(MAKE) test-env-image; \
	fi
	/bin/cp -f config/crd/bases/* deploy/charts/distort/crds/
	$(CONTAINER_TOOL) save "$(TEST_ENV_IMG)" -o vagrant/distort-img.tar
	@for node in $(VAGRANT_NODES); do \
		echo "Loading $(TEST_ENV_IMG) into $$node"; \
		(cd vagrant && vagrant ssh "$$node" -c "sudo k3s ctr images import /vagrant/distort-img.tar"); \
	done
	KUBECONFIG="$(LOCAL_KUBECONFIG)" $(KUBECTL) apply -f config/crd/bases/
	# CSIDriver.spec.attachRequired is immutable; recreate the isolated lab registration when upgrading fencing behavior.
	-KUBECONFIG="$(LOCAL_KUBECONFIG)" $(KUBECTL) delete csidriver storage.distort.io --ignore-not-found --wait=true --timeout=60s
	KUBECONFIG="$(LOCAL_KUBECONFIG)" helm upgrade --install distort ./deploy/charts/distort --namespace distort-system --create-namespace --values vagrant/helm-values.yaml --set image.pullPolicy=Never --set-string image.repository="$(TEST_ENV_IMAGE_REPOSITORY)" --set-string image.tag="$(TEST_ENV_IMAGE_TAG)"
	KUBECONFIG="$(LOCAL_KUBECONFIG)" $(KUBECTL) rollout restart -n distort-system deployment/distort-manager deployment/distort-csi-controller
	KUBECONFIG="$(LOCAL_KUBECONFIG)" $(KUBECTL) rollout restart -n distort-system daemonset/distort-agent daemonset/distort-csi-node
	KUBECONFIG="$(LOCAL_KUBECONFIG)" $(KUBECTL) rollout status -n distort-system deployment/distort-manager --timeout=180s
	KUBECONFIG="$(LOCAL_KUBECONFIG)" $(KUBECTL) rollout status -n distort-system deployment/distort-csi-controller --timeout=180s
	KUBECONFIG="$(LOCAL_KUBECONFIG)" $(KUBECTL) rollout status -n distort-system daemonset/distort-agent --timeout=180s
	KUBECONFIG="$(LOCAL_KUBECONFIG)" $(KUBECTL) rollout status -n distort-system daemonset/distort-csi-node --timeout=180s

.PHONY: test-env-redeploy
test-env-redeploy: test-env-deploy ## Alias for the normal edit/build/load/Helm-upgrade inner loop.

.PHONY: test-env-status
test-env-status: test-env-guard ## Show VMs, Kubernetes workloads, discovered hardware, and allocations.
	cd vagrant && vagrant status
	KUBECONFIG="$(LOCAL_KUBECONFIG)" $(KUBECTL) get nodes -o wide
	KUBECONFIG="$(LOCAL_KUBECONFIG)" $(KUBECTL) get pods -n distort-system -o wide
	KUBECONFIG="$(LOCAL_KUBECONFIG)" $(KUBECTL) get nvmedevices,rdmastoragenodes,nvmedeviceclaims,nvmepartitions -A -o wide

.PHONY: test-env-smoke
test-env-smoke: test-env-guard ## Verify three ready nodes, healthy rollouts, NVMe discovery, and RDMA discovery.
	KUBECTL="$(KUBECTL)" bash vagrant/smoke-test.sh "$(LOCAL_KUBECONFIG)"

.PHONY: test-env-logs
test-env-logs: test-env-guard ## Print recent logs from all DISTORT lab workloads.
	-KUBECONFIG="$(LOCAL_KUBECONFIG)" $(KUBECTL) logs -n distort-system -l app.kubernetes.io/name=distort --all-containers=true --prefix=true --tail=200

.PHONY: test-env-ssh
test-env-ssh: ## Open a shell on NODE (default: distort-worker-1).
	cd vagrant && vagrant ssh $(or $(NODE),distort-worker-1)

.PHONY: test-env-destroy
test-env-destroy: ## Destroy only the isolated Vagrant lab VMs.
	cd vagrant && vagrant destroy -f

.PHONY: test-e2e
test-e2e: get-kubeconfig manifests generate fmt vet ## Run the unified Ginkgo E2E tests against Vagrant K3s
	KUBECTL="$(KUBECTL)" bash vagrant/verify-local-kubeconfig.sh "$(LOCAL_KUBECONFIG)"
	$(MAKE) test-env-smoke
	KUBECONFIG="$(LOCAL_KUBECONFIG)" bash vagrant/verify-env.sh
	KUBECONFIG="$(LOCAL_KUBECONFIG)" go test -tags=e2e ./test/e2e/ -v -ginkgo.v $(E2E_ARGS)

.PHONY: test-e2e-regression
test-e2e-regression: get-kubeconfig manifests generate fmt vet ## Run quarantined Vagrant regressions (optionally FINDING=F1).
	KUBECTL="$(KUBECTL)" bash vagrant/verify-local-kubeconfig.sh "$(LOCAL_KUBECONFIG)"
	KUBECONFIG="$(LOCAL_KUBECONFIG)" DISTORT_RUN_KNOWN_FAILURES=1 DISTORT_FINDING="$(FINDING)" go test -tags=e2e ./test/e2e/ -v -ginkgo.v -ginkgo.label-filter=known-failure $(E2E_ARGS)

.PHONY: test-env-all
test-env-all: ## Reset the isolated lab, verify it, and run every green E2E test.
	$(MAKE) test-env-reset
	$(MAKE) test-env-smoke
	$(MAKE) test-e2e

.PHONY: test-env-regression
test-env-regression: ## Reset the lab and run quarantined E2E regressions (optionally FINDING=F1).
	$(MAKE) test-env-reset
	$(MAKE) test-env-smoke
	$(MAKE) test-e2e-regression FINDING="$(FINDING)"

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
build: manifests generate fmt vet ## Build manager, agent, and csi binaries.
	go build -o bin/distort-manager cmd/distort-manager/main.go
	go build -o bin/distort-agent cmd/distort-agent/main.go
	go build -o bin/distort-csi cmd/distort-csi/main.go

.PHONY: run
run: manifests generate fmt vet ## Run the manager controller from your host.
	go run ./cmd/distort-manager/main.go

# If you wish to build the manager image targeting other platforms you can use the --platform flag.
# (i.e. docker build --platform linux/arm64). However, you must enable docker buildKit for it.
# More info: https://docs.docker.com/develop/develop-images/build_enhancements/
.PHONY: docker-build
docker-build: ## Build docker image with the manager.
	$(CONTAINER_TOOL) build $(DOCKER_BUILD_ARGS) -t ${IMG} .

.PHONY: docker-push
docker-push: ## Push docker image with the manager.
	$(CONTAINER_TOOL) push ${IMG}


.PHONY: docker-build-push-multiarch
docker-build-push-multiarch: ## Build and push docker image for amd64 and arm64 architectures
	- $(CONTAINER_TOOL) buildx create --name distort-builder --use || true
	$(CONTAINER_TOOL) buildx build --push --platform=linux/amd64,linux/arm64 --tag ${IMG} .
	- $(CONTAINER_TOOL) buildx rm distort-builder || true



##@ Dependencies

## Location to install dependencies to
LOCALBIN ?= $(shell pwd)/bin
$(LOCALBIN):
	mkdir -p "$(LOCALBIN)"

## Tool Binaries
KUBECTL ?= kubectl
KIND ?= kind
CONTROLLER_GEN ?= $(LOCALBIN)/controller-gen
ENVTEST ?= $(LOCALBIN)/setup-envtest
GOLANGCI_LINT = $(LOCALBIN)/golangci-lint

## Tool Versions
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
GOLANGCI_LINT_BASE = $(LOCALBIN)/golangci-lint-$(GOLANGCI_LINT_VERSION)
GOLANGCI_LINT_CUSTOM = $(LOCALBIN)/golangci-lint-custom-$(GOLANGCI_LINT_VERSION)

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
$(GOLANGCI_LINT): $(GOLANGCI_LINT_CUSTOM)
	ln -sfn "$$(realpath "$(GOLANGCI_LINT_CUSTOM)")" "$(GOLANGCI_LINT)"

$(GOLANGCI_LINT_BASE): | $(LOCALBIN)
	GOBIN="$(LOCALBIN)" go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
	mv "$(LOCALBIN)/golangci-lint" "$(GOLANGCI_LINT_BASE)"

$(GOLANGCI_LINT_CUSTOM): $(GOLANGCI_LINT_BASE) .custom-gcl.yml
	@echo "Building custom golangci-lint with plugins..."
	"$(GOLANGCI_LINT_BASE)" custom --destination "$(LOCALBIN)" --name golangci-lint-custom
	mv "$(LOCALBIN)/golangci-lint-custom" "$(GOLANGCI_LINT_CUSTOM)"

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

##@ Documentation

.PHONY: docs-serve docs-build
docs-serve: ## Run Hugo documentation site locally.
	cd docs && hugo server -D

docs-build: ## Build Hugo documentation site static files.
	cd docs && hugo --minify
