# netbox-operator
#
# Every tool version is pinned below. A `@latest` anywhere in this file is a
# reproducibility bug: it lets a contributor's machine change generated output.

SHELL := /usr/bin/env bash
.DEFAULT_GOAL := help
.SHELLFLAGS := -ec

IMG ?= netbox-operator:latest

# Tool versions -- pinned, never @latest.
CONTROLLER_TOOLS_VERSION ?= v0.19.0
KUSTOMIZE_VERSION        ?= v5.5.0
GOLANGCI_LINT_VERSION    ?= v2.6.1
ENVTEST_VERSION          ?= release-0.22
ENVTEST_K8S_VERSION      ?= 1.34.0

LOCALBIN ?= $(shell pwd)/bin
CONTROLLER_GEN ?= $(LOCALBIN)/controller-gen
KUSTOMIZE      ?= $(LOCALBIN)/kustomize
GOLANGCI_LINT  ?= $(LOCALBIN)/golangci-lint
ENVTEST        ?= $(LOCALBIN)/setup-envtest

$(LOCALBIN):
	mkdir -p $(LOCALBIN)

##@ General

.PHONY: help
help: ## Show this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} \
		/^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2 } \
		/^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) }' $(MAKEFILE_LIST)

##@ Development

.PHONY: generate
generate: controller-gen ## Generate DeepCopy methods.
	$(CONTROLLER_GEN) object:headerFile="hack/boilerplate.go.txt" paths="./api/..."


.PHONY: manifests
manifests: controller-gen ## Generate CRDs and RBAC into config/.
	@# paths=./... rather than listing ./api/... and ./internal/... separately:
	@# controller-gen fails hard on a path pattern that matches no package, and git does
	@# not track empty directories, so a fresh checkout has no ./internal/... until the
	@# first controller lands. Found by CI on exactly that clean checkout.
	$(CONTROLLER_GEN) rbac:roleName=manager-role crd \
		paths="./..." \
		output:crd:artifacts:config=config/crd/bases \
		output:rbac:artifacts:config=config/rbac
	@# The Secret grant controller-gen cannot express: a marker only ever produces a
	@# cluster-wide rule, and the namespaces holding endpoint credentials are deploy-time
	@# configuration. Generated from config/rbac/credential-namespaces/namespaces.txt into
	@# config/rbac, so `make verify` covers it like anything else generated (NBO-072).
	./hack/credential-rbac.sh
	@# The nullable flag controller-gen cannot express: `nullable` is a field marker and the
	@# nullable thing is spec.customFields' map *values*, whose null means "remove this
	@# custom field" (#196). Without it the API server prunes the null before validation.
	./hack/crd-nullable.sh

.PHONY: fmt
fmt: ## Format the Go code.
	go fmt ./...

.PHONY: vet
vet: ## Run go vet.
	go vet ./...

.PHONY: lint
lint: golangci-lint ## Run golangci-lint.
	$(GOLANGCI_LINT) run

.PHONY: lint-fix
lint-fix: golangci-lint ## Run golangci-lint and apply fixes.
	$(GOLANGCI_LINT) run --fix

.PHONY: test
test: manifests generate fmt vet envtest ## Run unit tests and envtest.
	KUBEBUILDER_ASSETS="$(shell $(ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir $(LOCALBIN) -p path)" \
		go test $$(go list ./... | grep -v /test/e2e) -coverprofile cover.out

.PHONY: lint-schema
lint-schema: ## Lint hack/*.py with ruff, the same check CI runs.
	@if command -v ruff >/dev/null 2>&1; then \
		ruff check hack/; \
	else \
		echo "ruff not installed (pip install ruff); CI will still run it."; \
	fi

.PHONY: test-schema
test-schema: lint-schema ## Run the schema extraction pipeline against test/fixtures/netbox-models.
	python3 hack/test_digest.py

.PHONY: test-e2e
test-e2e: ## Run e2e tests against a kind cluster and a live NetBox.
	@if [ -z "$$(find test/e2e -name '*_test.go' 2>/dev/null)" ]; then \
		echo "No e2e suite yet -- the harness lands with NBO-017. Skipping."; \
		exit 0; \
	fi
	go test ./test/e2e/ -v -ginkgo.v

# Paths whose contents are produced by controller-gen. Listed explicitly so that a dirty
# working tree does not read as stale codegen: the two failures have different fixes, and
# conflating them makes the target useless locally.
GENERATED_PATHS ?= config/crd config/rbac api/v1alpha1/zz_generated.deepcopy.go

.PHONY: verify
verify: manifests generate ## Fail if generated output is not committed.
	@git diff --exit-code -- $(GENERATED_PATHS) || { \
		echo ""; \
		echo "Generated output is stale. Run 'make manifests generate' and commit the result."; \
		exit 1; }

##@ Build

.PHONY: build
build: manifests generate fmt vet ## Build the manager binary.
	go build -o bin/manager ./cmd/manager

.PHONY: run
run: manifests generate fmt vet ## Run the manager against the current kubeconfig.
	go run ./cmd/manager

.PHONY: docker-build
docker-build: ## Build the manager image.
	docker build -t $(IMG) .

.PHONY: clean
clean: ## Remove build output.
	@# envtest installs its binaries read-only, so make them writable before removing.
	-chmod -R u+w $(LOCALBIN) 2>/dev/null
	rm -rf $(LOCALBIN) cover.out cover.html

##@ Deployment

.PHONY: install
install: manifests kustomize ## Install CRDs into the current cluster.
	$(KUSTOMIZE) build config/crd | kubectl apply -f -

.PHONY: uninstall
uninstall: manifests kustomize ## Remove CRDs from the current cluster.
	$(KUSTOMIZE) build config/crd | kubectl delete --ignore-not-found -f -

.PHONY: deploy
deploy: manifests kustomize ## Deploy the manager to the current cluster.
	cd config/manager && $(KUSTOMIZE) edit set image controller=$(IMG)
	$(KUSTOMIZE) build config/default | kubectl apply -f -

.PHONY: undeploy
undeploy: kustomize ## Remove the manager from the current cluster.
	$(KUSTOMIZE) build config/default | kubectl delete --ignore-not-found -f -

##@ Tools

.PHONY: controller-gen
controller-gen: $(CONTROLLER_GEN)
$(CONTROLLER_GEN): $(LOCALBIN)
	$(call go-install-tool,$(CONTROLLER_GEN),sigs.k8s.io/controller-tools/cmd/controller-gen,$(CONTROLLER_TOOLS_VERSION))

.PHONY: kustomize
kustomize: $(KUSTOMIZE)
$(KUSTOMIZE): $(LOCALBIN)
	$(call go-install-tool,$(KUSTOMIZE),sigs.k8s.io/kustomize/kustomize/v5,$(KUSTOMIZE_VERSION))

.PHONY: golangci-lint
golangci-lint: $(GOLANGCI_LINT)
$(GOLANGCI_LINT): $(LOCALBIN)
	$(call go-install-tool,$(GOLANGCI_LINT),github.com/golangci/golangci-lint/v2/cmd/golangci-lint,$(GOLANGCI_LINT_VERSION))

.PHONY: envtest
envtest: $(ENVTEST)
$(ENVTEST): $(LOCALBIN)
	$(call go-install-tool,$(ENVTEST),sigs.k8s.io/controller-runtime/tools/setup-envtest,$(ENVTEST_VERSION))

# go-install-tool installs $2@$3 as $1, version-suffixing the binary so a version
# bump reinstalls instead of silently reusing the old one.
define go-install-tool
@[ -f "$(1)-$(3)" ] || { \
set -e; \
package=$(2)@$(3) ;\
echo "Downloading $${package}" ;\
rm -f $(1) || true ;\
GOBIN=$(LOCALBIN) go install $${package} ;\
mv $(1) $(1)-$(3) ;\
} ;\
ln -sf $(1)-$(3) $(1)
endef
