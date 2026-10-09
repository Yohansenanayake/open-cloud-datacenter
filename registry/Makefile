# Registry operator — provisions per-tenant Harbor registries via CRDs.
# Layout follows the KeyVault operator (crds/keyvault) conventions.

IMG ?= controller:latest
# Release version baked into the binary and reported in its startup log. Keep
# it equal to the image tag and to charts/chart/Chart.yaml's appVersion.
VERSION ?= dev
CONTAINER_TOOL ?= docker

# Tooling
LOCALBIN ?= $(shell pwd)/bin
KUBECTL ?= kubectl
KUSTOMIZE ?= $(LOCALBIN)/kustomize
CONTROLLER_GEN ?= $(LOCALBIN)/controller-gen

## Tool Versions — kept in sync with the keyvault/database operators' Makefiles.
KUSTOMIZE_VERSION ?= v5.8.1
CONTROLLER_TOOLS_VERSION ?= v0.20.1

.PHONY: all
all: build

##@ General

.PHONY: help
help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) }' $(MAKEFILE_LIST)

##@ Development

.PHONY: manifests
manifests: controller-gen ## Generate CRDs and RBAC into config/.
	"$(CONTROLLER_GEN)" rbac:roleName=manager-role paths="./..."
	"$(CONTROLLER_GEN)" crd paths="./api/..." output:crd:artifacts:config=config/crd/bases

.PHONY: generate
generate: controller-gen ## Generate DeepCopy methods.
	"$(CONTROLLER_GEN)" object:headerFile="hack/boilerplate.go.txt" paths="./api/..."

.PHONY: fmt
fmt: ## Run go fmt.
	go fmt ./...

.PHONY: vet
vet: ## Run go vet.
	go vet ./...

.PHONY: test
test: fmt vet ## Run unit tests.
	go test ./... -coverprofile cover.out

##@ Build

.PHONY: build
build: fmt vet ## Build the provisioner binary.
	go build -o bin/registry-provisioner ./cmd

.PHONY: run
run: fmt vet ## Run the provisioner from your host.
	go run ./cmd

.PHONY: docker-build
docker-build: ## Build the container image ($(IMG)).
	$(CONTAINER_TOOL) build --build-arg VERSION=$(VERSION) -t ${IMG} .

.PHONY: docker-push
docker-push: ## Push the container image ($(IMG)).
	$(CONTAINER_TOOL) push ${IMG}

.PHONY: build-installer
build-installer: manifests generate kustomize ## Generate the operator's deployment, without the CRD, into dist/install.yaml (the helm plugin's input).
	mkdir -p dist
	cd config/manager && "$(KUSTOMIZE)" edit set image controller=${IMG}
	"$(KUSTOMIZE)" build config/default > dist/install.yaml

##@ Deployment

ifndef ignore-not-found
  ignore-not-found = false
endif

.PHONY: install
install: kustomize ## Install the CRD into the cluster (KUBECONFIG / --context as needed).
	"$(KUSTOMIZE)" build config/crd | "$(KUBECTL)" apply -f -

.PHONY: uninstall
uninstall: kustomize ## DESTRUCTIVE. Remove the CRD, which deletes every Registry and every Harbor project with it. Deleting the Registries first is the supported way to do this deliberately.
	"$(KUSTOMIZE)" build config/crd | "$(KUBECTL)" delete --ignore-not-found=$(ignore-not-found) -f -

.PHONY: deploy
deploy: install ## Deploy the operator (CRD, namespace, RBAC, manager) with generic placeholder config. Override IMG as needed.
	cd config/manager && "$(KUSTOMIZE)" edit set image controller=${IMG}
	"$(KUSTOMIZE)" build config/default | "$(KUBECTL)" apply -f -

.PHONY: undeploy
undeploy: kustomize ## Remove the operator. The CRD and every Registry stay; see `make uninstall` to remove those deliberately.
	"$(KUSTOMIZE)" build config/default | "$(KUBECTL)" delete --ignore-not-found=$(ignore-not-found) -f -

.PHONY: deploy-local
deploy-local: kustomize ## Deploy using config/local — your real HARBOR_URL and credentials Secret (see config/local/manager_local_patch.yaml.example).
	@test -f config/local/manager_local_patch.yaml || { \
		echo "config/local/manager_local_patch.yaml not found."; \
		echo "Copy config/local/manager_local_patch.yaml.example to config/local/manager_local_patch.yaml and fill in your real values first."; \
		exit 1; \
	}
	"$(KUSTOMIZE)" build config/crd | "$(KUBECTL)" apply -f -
	"$(KUSTOMIZE)" build config/local | "$(KUBECTL)" apply -f -

.PHONY: undeploy-local
undeploy-local: kustomize ## Remove the operator deployed via deploy-local.
	"$(KUSTOMIZE)" build config/local | "$(KUBECTL)" delete --ignore-not-found=$(ignore-not-found) -f -

##@ Tools

$(LOCALBIN):
	mkdir -p "$(LOCALBIN)"

.PHONY: kustomize
kustomize: $(KUSTOMIZE) ## Download kustomize locally if necessary.
$(KUSTOMIZE): $(LOCALBIN)
	$(call go-install-tool,$(KUSTOMIZE),sigs.k8s.io/kustomize/kustomize/v5,$(KUSTOMIZE_VERSION))

.PHONY: controller-gen
controller-gen: $(CONTROLLER_GEN) ## Download controller-gen locally if necessary.
$(CONTROLLER_GEN): $(LOCALBIN)
	$(call go-install-tool,$(CONTROLLER_GEN),sigs.k8s.io/controller-tools/cmd/controller-gen,$(CONTROLLER_TOOLS_VERSION))

# go-install-tool will 'go install' any package with custom target and name of
# binary, if it doesn't exist. Matches keyvault/database's Makefile exactly.
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

##@ Helm Deployment

## Helm binary to use for deploying the chart
HELM ?= helm
## Namespace to deploy the Helm release
HELM_NAMESPACE ?= registry-system
## Name of the Helm release
HELM_RELEASE ?= registry-operator
## Path to the Helm chart directory
HELM_CHART_DIR ?= charts/chart
## Additional arguments to pass to helm commands
HELM_EXTRA_ARGS ?=

.PHONY: install-helm
install-helm: ## Check that Helm is installed.
	@command -v $(HELM) >/dev/null 2>&1 || { \
		echo "Helm not found. Install a released version: https://helm.sh/docs/intro/install/"; \
		exit 1; \
	}

.PHONY: helm-deploy
helm-deploy: install-helm ## Deploy manager via Helm. Specify an image with IMG and Harbor with HELM_EXTRA_ARGS="--set harbor.url=https://...".
	@img='$(IMG)'; \
	last_segment="$${img##*/}"; \
	if [ "$${last_segment}" != "$${last_segment%:*}" ]; then \
		repo="$${img%:*}"; tag="$${img##*:}"; \
	else \
		repo="$$img"; tag="latest"; \
	fi; \
	$(HELM) upgrade --install $(HELM_RELEASE) $(HELM_CHART_DIR) \
		--namespace $(HELM_NAMESPACE) \
		--create-namespace \
		--set manager.image.repository="$$repo" \
		--set manager.image.tag="$$tag" \
		--wait \
		--timeout 5m \
		$(HELM_EXTRA_ARGS)

.PHONY: helm-uninstall
helm-uninstall: ## Uninstall the Helm release from the K8s cluster.
	$(HELM) uninstall $(HELM_RELEASE) --namespace $(HELM_NAMESPACE)

.PHONY: helm-status
helm-status: ## Show Helm release status.
	$(HELM) status $(HELM_RELEASE) --namespace $(HELM_NAMESPACE)

.PHONY: helm-history
helm-history: ## Show Helm release history.
	$(HELM) history $(HELM_RELEASE) --namespace $(HELM_NAMESPACE)

.PHONY: helm-rollback
helm-rollback: ## Rollback to previous Helm release.
	$(HELM) rollback $(HELM_RELEASE) --namespace $(HELM_NAMESPACE)
