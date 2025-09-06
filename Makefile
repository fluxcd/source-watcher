# Image URL to use all building/pushing image targets
IMG ?= fluxcd/source-watcher:latest
# Produce CRDs that work back to Kubernetes 1.16
CRD_OPTIONS ?= crd:crdVersions=v1

REPOSITORY_ROOT := $(shell git rev-parse --show-toplevel)
BUILD_DIR := $(REPOSITORY_ROOT)/build

# Get the currently used golang install path (in GOPATH/bin, unless GOBIN is set)
ifeq (,$(shell go env GOBIN))
GOBIN=$(shell go env GOPATH)/bin
else
GOBIN=$(shell go env GOBIN)
endif

# Allows for defining additional Docker buildx arguments, e.g. '--push'.
BUILD_ARGS ?=
# Architectures to build images for.
BUILD_PLATFORMS ?= linux/amd64

# Architecture to use envtest with
ENVTEST_ARCH ?= amd64

# API generation utilities
CONTROLLER_GEN_VERSION ?= v0.19.0

# Download source-controller CRDs
SOURCE_VER ?= $(shell go list -m all | grep github.com/fluxcd/source-controller/api | awk '{print $$2}')
SOURCE_CRD_VER=$(BUILD_DIR)/.src-crd-$(SOURCE_VER)
GITREPO_CRD ?= config/crd/bases/gitrepositories.yaml
BUCKET_CRD ?= config/crd/bases/buckets.yaml
OCIREPO_CRD ?= config/crd/bases/ocirepositories.yaml
EA_CRD ?= config/crd/bases/externalartifacts.yaml

all: manager

# Run tests
KUBEBUILDER_ASSETS?="$(shell $(ENVTEST) --arch=$(ENVTEST_ARCH) use -i $(ENVTEST_KUBERNETES_VERSION) --bin-dir=$(ENVTEST_ASSETS_DIR) -p path)"
test: tidy generate fmt vet manifests install-envtest download-crd-deps
	KUBEBUILDER_ASSETS=$(KUBEBUILDER_ASSETS) go test ./... -coverprofile cover.out

# Build manager binary
manager: generate fmt vet
	go build -o bin/manager main.go

# Run against the configured Kubernetes cluster in ~/.kube/config
run: generate fmt vet manifests
	go run ./main.go


# Delete previously downloaded CRDs and record the new version of the source
# CRDs.
$(SOURCE_CRD_VER):
	rm -f $(BUILD_DIR)/.src-crd*
	$(MAKE) cleanup-crd-deps
	if ! test -d "$(BUILD_DIR)"; then mkdir -p $(BUILD_DIR); fi
	touch $(SOURCE_CRD_VER)

$(GITREPO_CRD):
	curl -s https://raw.githubusercontent.com/fluxcd/source-controller/${SOURCE_VER}/config/crd/bases/source.toolkit.fluxcd.io_gitrepositories.yaml -o $(GITREPO_CRD)

$(BUCKET_CRD):
	curl -s https://raw.githubusercontent.com/fluxcd/source-controller/${SOURCE_VER}/config/crd/bases/source.toolkit.fluxcd.io_buckets.yaml -o $(BUCKET_CRD)

$(OCIREPO_CRD):
	curl -s https://raw.githubusercontent.com/fluxcd/source-controller/${SOURCE_VER}/config/crd/bases/source.toolkit.fluxcd.io_ocirepositories.yaml -o $(OCIREPO_CRD)

$(EA_CRD):
	curl -s https://raw.githubusercontent.com/fluxcd/source-controller/${SOURCE_VER}/config/crd/bases/source.toolkit.fluxcd.io_externalartifacts.yaml -o $(EA_CRD)

# Download the CRDs the controller depends on
download-crd-deps: $(SOURCE_CRD_VER) $(GITREPO_CRD) $(BUCKET_CRD) $(OCIREPO_CRD) $(EA_CRD)

# Delete the downloaded CRD dependencies.
cleanup-crd-deps:
	rm -f $(GITREPO_CRD) $(BUCKET_CRD) $(OCIREPO_CRD) $(EA_CRD)

# Install CRDs into a cluster
install: manifests
	kustomize build config/crd | kubectl apply -f -

# Uninstall CRDs from a cluster
uninstall: manifests
	kustomize build config/crd | kubectl delete -f -

# Deploy controller in the configured Kubernetes cluster in ~/.kube/config
deploy: manifests
	cd config/manager && kustomize edit set image source-watcher=${IMG}
	kustomize build config/default | kubectl apply -f -

# Generate manifests e.g. CRD, RBAC etc.
manifests: controller-gen
	$(CONTROLLER_GEN) $(CRD_OPTIONS) rbac:roleName=source-reader paths="./..." output:crd:artifacts:config=config/crd/bases

# Run go tidy to cleanup go.mod
tidy:
	rm -f go.sum; go mod tidy -compat=1.25

# Run go fmt against code
fmt:
	go fmt ./...

# Run go vet against code
vet:
	go vet ./...

# Generate code
generate: controller-gen
	$(CONTROLLER_GEN) object:headerFile="hack/boilerplate.go.txt" paths="./..."

# Build the docker image
docker-build:
	docker buildx build \
	--platform=$(BUILD_PLATFORMS) \
	-t ${IMG} \
	--load \
	${BUILD_ARGS} .

# Push the docker image
docker-push:
	docker push ${IMG}

manifests-release:
	mkdir -p ./build
	mkdir -p config/release-tmp && cp config/release/* config/release-tmp
	cd config/release-tmp && kustomize edit set image fluxcd/source-watcher=${IMG}
	kustomize build config/release-tmp > ./build/source-watcher.deployment.yaml
	rm -rf config/release-tmp

# Find or download controller-gen
CONTROLLER_GEN = $(shell pwd)/bin/controller-gen
.PHONY: controller-gen
controller-gen: ## Download controller-gen locally if necessary.
	$(call go-install-tool,$(CONTROLLER_GEN),sigs.k8s.io/controller-tools/cmd/controller-gen@$(CONTROLLER_GEN_VERSION))

ENVTEST_ASSETS_DIR=$(shell pwd)/testbin
ENVTEST_KUBERNETES_VERSION?=latest
install-envtest: setup-envtest
	mkdir -p ${ENVTEST_ASSETS_DIR}
	$(ENVTEST) use $(ENVTEST_KUBERNETES_VERSION) --arch=$(ENVTEST_ARCH) --bin-dir=$(ENVTEST_ASSETS_DIR)

ENVTEST = $(shell pwd)/bin/setup-envtest
.PHONY: envtest
setup-envtest: ## Download envtest-setup locally if necessary.
	$(call go-install-tool,$(ENVTEST),sigs.k8s.io/controller-runtime/tools/setup-envtest@latest)

# go-install-tool will 'go install' any package $2 and install it to $1.
PROJECT_DIR := $(shell dirname $(abspath $(lastword $(MAKEFILE_LIST))))
define go-install-tool
@[ -f $(1) ] || { \
set -e ;\
TMP_DIR=$$(mktemp -d) ;\
cd $$TMP_DIR ;\
go mod init tmp ;\
echo "Downloading $(2)" ;\
GOBIN=$(PROJECT_DIR)/bin go install $(2) ;\
rm -rf $$TMP_DIR ;\
}
endef
