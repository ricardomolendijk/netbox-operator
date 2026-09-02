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
# Unlike every other pin here, this one is tied to the Go toolchain and not only to the tool's
# own behaviour: golangci-lint links its own copy of the type-checker (through x/tools) and can
# only read the export data written by the Go releases it was built for. v2.6.1 links x/tools
# v0.38.0, which stops at export data version 2; Go 1.27 writes version 4, so on a 1.27 machine
# the linter failed every import of the standard library with "export data version 4 is greater
# than maximum supported version 2" and linted nothing at all. The pin therefore stopped meaning
# "local and CI agree" and started meaning "only CI can run this" -- which is how #275 merged
# with a one-line prealloc finding its author had no way to see (#283, and #279 to repair it).
# v2.13.0 added Go 1.27 support; v2.13.2 is that line's current patch. It still declares Go
# 1.26.0 as its minimum, so it runs on the toolchain go.mod pins for CI as well as on a
# contributor's 1.27.
GOLANGCI_LINT_VERSION    ?= v2.13.2
ENVTEST_VERSION          ?= release-0.22
ENVTEST_K8S_VERSION      ?= 1.34.0
KIND_VERSION             ?= v0.30.0
# Equal to the version .github/workflows/ci.yaml and release.yaml install, so the chart the
# e2e suite deploys is never first rendered by a different Helm than the one that packages it.
HELM_VERSION             ?= v3.16.3
# Pulls mkdocs itself as a transitive pin, so a mkdocs release cannot change the site.
MKDOCS_MATERIAL_VERSION  ?= 9.7.7
MKDOCS                   ?= mkdocs

LOCALBIN ?= $(shell pwd)/bin
CONTROLLER_GEN ?= $(LOCALBIN)/controller-gen
KUSTOMIZE      ?= $(LOCALBIN)/kustomize
GOLANGCI_LINT  ?= $(LOCALBIN)/golangci-lint
ENVTEST        ?= $(LOCALBIN)/setup-envtest
KIND           ?= $(LOCALBIN)/kind
HELM_BIN       ?= $(LOCALBIN)/helm

# Where controller-gen writes the CRDs before hack/crd-nullable.sh publishes them into
# config/crd/bases. Under $(LOCALBIN) because it is generator scratch rather than output:
# bin/ is already gitignored and already what `make clean` takes away, and a staging
# directory under config/ would be picked up by `make verify`'s tree.
CRD_STAGE      ?= $(LOCALBIN)/crd-stage

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
	@# The CRDs go to a staging directory and hack/crd-nullable.sh publishes them from
	@# there, so that a CRD only ever appears in config/crd/bases with the nullable flag
	@# already on it. Writing them straight into config/crd/bases published every CRD
	@# incorrect for the couple of seconds the post-passes take, which is long enough for a
	@# concurrent envtest suite to install one and fail on a feature that works (#276); the
	@# script's own header carries the full reasoning. Emptied first, because controller-gen
	@# only ever writes: a leftover CRD for a deleted Kind would otherwise be republished
	@# for ever.
	rm -rf $(CRD_STAGE)
	$(CONTROLLER_GEN) rbac:roleName=manager-role crd webhook \
		paths="./..." \
		output:crd:artifacts:config=$(CRD_STAGE) \
		output:rbac:artifacts:config=config/rbac \
		output:webhook:artifacts:config=config/webhook
	@# The Secret grant controller-gen cannot express: a marker only ever produces a
	@# cluster-wide rule, and the namespaces holding endpoint credentials are deploy-time
	@# configuration. Generated from config/rbac/credential-namespaces/namespaces.txt into
	@# config/rbac, so `make verify` covers it like anything else generated (NBO-072).
	./hack/credential-rbac.sh
	@# The nullable flag controller-gen cannot express: `nullable` is a field marker and the
	@# nullable thing is spec.customFields' map *values*, whose null means "remove this
	@# custom field" (#196). Without it the API server prunes the null before validation.
	./hack/crd-nullable.sh $(CRD_STAGE)
	@# The chart's copies of the two things config/ generates: the CRDs and the manager
	@# ClusterRole's rules. Hand-maintaining 22 CRDs and a rule list that grows with every
	@# kind is wrong within one release, so it is a copy and `make verify` checks it.
	./hack/helm-sync.sh

.PHONY: fmt
fmt: ## Format the Go code.
	go fmt ./...

.PHONY: vet
vet: ## Run go vet.
	go vet ./...

.PHONY: lint
lint: golangci-lint ## Run golangci-lint.
	@# The run goes through a log so the failure the pin above describes can be translated
	@# the next time it happens -- and it will happen again, because roughly once a year a Go
	@# release changes the export data format and every linter built before it stops being
	@# able to typecheck anything. The diagnostic golangci-lint emits for that names neither
	@# Go nor golangci-lint: it reports the standard-library imports of some package nobody
	@# touched as typecheck errors and gives up, which reads as a broken repository rather
	@# than a stale tool. It read that way for long enough to merge #275 with lint red, so
	@# say which two versions disagree and which line moves, rather than leaving the next
	@# contributor to decode it (#283).
	@# --color is passed explicitly because tee makes stdout a pipe, and the linter would
	@# otherwise drop the colour it prints when run from a terminal.
	@set -o pipefail; \
	if [ -t 1 ]; then color=always; else color=never; fi; \
	log=$$(mktemp); \
	trap 'rm -f "$$log"' EXIT; \
	if $(GOLANGCI_LINT) run --color=$$color 2>&1 | tee "$$log"; then exit 0; fi; \
	if grep -q 'export data version' "$$log"; then \
		echo; \
		echo "The linter typechecked nothing above: it cannot read this Go toolchain."; \
		echo "  local toolchain:  $$(go version)"; \
		echo "  pinned linter:    golangci-lint $(GOLANGCI_LINT_VERSION), built against an older Go"; \
		echo "golangci-lint carries its own copy of the Go type-checker, so a Go release that"; \
		echo "changes the export data format makes an older linter unusable. The errors above"; \
		echo "are the standard library failing to import; they are not your change."; \
		echo "Fix: raise GOLANGCI_LINT_VERSION in the Makefile to a release that supports this"; \
		echo "Go (https://github.com/golangci/golangci-lint/releases), send that bump as its own"; \
		echo "PR, and keep go.mod's toolchain and CI on the same Go. Do not reach for nolint."; \
	fi; \
	exit 1

.PHONY: lint-fix
lint-fix: golangci-lint ## Run golangci-lint and apply fixes.
	$(GOLANGCI_LINT) run --fix

.PHONY: test
test: manifests generate fmt vet envtest ## Run unit tests and envtest.
	@# The anchored pattern excludes the e2e *suite* -- which needs Docker, a kind cluster and
	@# a live NetBox -- and keeps test/e2e/harness, whose canonical NetBox dump and fixture
	@# ordering are pure functions and are the load-bearing half of the ordering gate. Those
	@# have to be regression-tested where the tests run on every PR, not only nightly.
	KUBEBUILDER_ASSETS="$(shell $(ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir $(LOCALBIN) -p path)" \
		go test $$(go list ./... | grep -v '/test/e2e$$') -coverprofile cover.out

# The IR the generator reads. Committed and version-stamped, so `make gen-kinds` needs no
# NetBox checkout and no network (docs/regenerating.md).
IR ?= hack/testdata/ir-4.6.8.json.gz

.PHONY: gen-kinds
gen-kinds: ## Emit the generated Kinds from the IR, then regenerate deepcopy and CRDs.
	go run ./hack/gen-types -ir $(IR)
	$(MAKE) generate manifests

# Not wired into `verify` yet, and deliberately: every NetBox kind in the tree today is
# hand-written, so there is nothing committed for it to check. NBO-043 emits the M3/M4 kinds
# for real and adds this to `verify` and to CI in the same change.
.PHONY: gen-check
gen-check: ## Fail if any generated Kind differs from what the IR would produce.
	go run ./hack/gen-types -ir $(IR) -check

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
	@# The API half of the same pipeline: choice values, writable-vs-read-only, and the query
	@# parameters each Kind's filterset registers, merged into one IR (NBO-041).
	python3 hack/test_ir.py

.PHONY: coverage
coverage: ## Regenerate docs/coverage.md and print the coverage summary.
	@# The audit itself is a Go test, because the implemented kind set lives in
	@# internal/registry and asking the registry is the only reading that cannot disagree
	@# with the running operator. -update rewrites the document; without it the same test
	@# compares against the committed copy, which is how `make test` and CI gate coverage
	@# without a second workflow step.
	go test ./internal/registry/ -run TestCoverage -count=1 -update
	@awk '/^## Summary/{f=1} /^## Uncovered/{f=0} f' docs/coverage.md
	@echo "Full table: docs/coverage.md"

# The suite applies the graph, tears it down and does it again for every permutation, against
# a real NetBox. Twenty of those do not fit in `go test`'s 10-minute default.
E2E_TIMEOUT ?= 150m

.PHONY: test-e2e
test-e2e: ## Run e2e tests against a kind cluster and a live NetBox.
	@# One recipe line, not two. `exit 0` in a separate line's shell exits that shell and
	@# make runs the next one anyway, so the skip printed its message and then failed on the
	@# step after it. The same shape now guards the Docker check: no daemon means skip, and
	@# skip has to mean the target succeeds.
	@#
	@# kind and helm are installed inside the branch that is going to use them, so a machine
	@# with no Docker skips without downloading 30 MB of tools it cannot use -- and so that a
	@# failed download is a failure of a run that was going to happen anyway.
	@# -maxdepth 1: the question is whether the *suite* package is here, and test/e2e/harness
	@# has tests of its own that `make test` runs. Without the bound, a checkout carrying the
	@# harness and no suite would run `go test ./test/e2e/` against a directory with no Go
	@# files in it, which is a build error rather than a skip.
	@if [ -z "$$(find test/e2e -maxdepth 1 -name '*_test.go' 2>/dev/null)" ]; then \
		echo "No e2e suite in test/e2e. Skipping."; \
	elif ! docker info >/dev/null 2>&1; then \
		echo "e2e needs a Docker daemon for kind and for NetBox, and none is reachable."; \
		echo "See docs/operations/e2e.md. Skipping."; \
	else \
		$(MAKE) kind helm-bin; \
		go test ./test/e2e/ -v -ginkgo.v -timeout $(E2E_TIMEOUT); \
	fi

# Paths whose contents are produced by controller-gen. Listed explicitly so that a dirty
# working tree does not read as stale codegen: the two failures have different fixes, and
# conflating them makes the target useless locally.
# charts/netbox-operator/crds is deliberately absent: the chart no longer contains the CRDs
# (#265), and hack/helm-sync.sh fails outright if one reappears.
GENERATED_PATHS ?= config/crd config/rbac config/webhook/manifests.yaml \
                  api/v1alpha1/zz_generated.deepcopy.go \
                  charts/netbox-operator/templates/clusterrole.yaml \
                  charts/netbox-operator/templates/webhook.yaml

.PHONY: verify
verify: manifests generate ## Fail if generated output is not committed.
	@git diff --exit-code -- $(GENERATED_PATHS) || { \
		echo ""; \
		echo "Generated output is stale. Run 'make manifests generate' and commit the result."; \
		exit 1; }

##@ Documentation

# The docs site is built from docs/ and nothing else -- see the allowlist note in
# mkdocs.yml. Its output (site/) is gitignored and never committed, so it is deliberately
# absent from GENERATED_PATHS: there is no checked-in artefact for `make verify` to police,
# and no generated navigation file that could drift from docs/README.md.

.PHONY: docs-tools
docs-tools: ## Install the pinned docs site toolchain into the active Python environment.
	python3 -m pip install "mkdocs-material==$(MKDOCS_MATERIAL_VERSION)"

.PHONY: docs-check
docs-check: ## Check every relative link and heading anchor under docs/ resolves.
	python3 hack/check-docs-links.py

.PHONY: docs-build
docs-build: ## Build the docs site into site/, then assert what it published.
	@command -v $(MKDOCS) >/dev/null || { \
		echo "mkdocs not found. Run 'make docs-tools' (mkdocs-material==$(MKDOCS_MATERIAL_VERSION))."; \
		exit 1; }
	@# --strict turns MkDocs' link and anchor warnings into errors, so a relative link that
	@# resolves on GitHub but not in the site fails the build instead of shipping a 404.
	$(MKDOCS) build --strict
	@# The acceptance criterion: nothing outside docs/ reached the output.
	python3 hack/check-docs-links.py --site site

.PHONY: docs-serve
docs-serve: ## Serve the docs site locally on :8000 with live reload.
	$(MKDOCS) serve

##@ Build

.PHONY: build
build: manifests generate fmt vet ## Build the manager binary.
	go build -o bin/manager ./cmd/manager

.PHONY: build-nbctl
build-nbctl: fmt vet ## Build the nbctl CLI.
	go build -o bin/nbctl ./cmd/nbctl

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
	rm -rf $(LOCALBIN) cover.out cover.html site

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

##@ Chart

CHART ?= charts/netbox-operator
# The pinned Helm when there is one, otherwise whatever is on PATH.
#
# `make helm-verify` compares against a golden RBAC render, and Helm 4 emits trailing blank
# lines Helm 3 does not -- so an ambient v4.2.4 fails the target on pure formatting while CI,
# which installs the pinned v3.16.3 onto PATH, passes. Preferring ./bin/helm fixes that for
# anyone who has run `make helm-bin` once.
#
# Conditional rather than a hard $(HELM_BIN): CI installs Helm to /usr/local/bin and never
# populates ./bin, so pointing at it unconditionally breaks the chart job with "No such file
# or directory" -- which is exactly what happened on the first attempt at this change.
HELM  ?= $(shell test -x "$(HELM_BIN)" && echo "$(HELM_BIN)" || echo helm)

# Chart.yaml is the one place the version is written (see .github/workflows/release.yaml),
# so the packaged filename and the CRD bundle's read it from there rather than repeat it.
CHART_VERSION = $(shell sed -n 's/^version: *//p' $(CHART)/Chart.yaml)

# The API group the CRDs register. Rendering off a cluster has to be told it exists, because
# templates/crds-precondition.yaml refuses to render an install whose CRDs are missing and
# `helm template` sees no discovery.
CRD_API_VERSION ?= netbox.kubeforge.org/v1alpha1

# The value files `helm lint` and `helm template` run over in CI: the defaults, and
# everything switched on at once. Between them every `if` in templates/ is taken both ways.
CHART_VALUES ?= $(CHART)/values.yaml $(CHART)/ci/all-features-values.yaml

.PHONY: helm-lint
helm-lint: ## helm lint the chart over the default and the all-features values.
	@# crds.check=false because `helm lint` has no --api-versions flag -- unlike
	@# `helm template` it cannot be told the operator's CRDs exist, so the precondition
	@# would fail every lint on every machine. helm-template below covers the check itself,
	@# both ways round.
	@for values in $(CHART_VALUES); do \
		echo "==> $$values"; \
		$(HELM) lint $(CHART) -f $$values --set crds.check=false || exit 1; \
	done

.PHONY: helm-template
helm-template: ## Render the chart over both value files, to /dev/null.
	@# --api-versions so the branches gated on a CRD existing are rendered rather than
	@# skipped: the ServiceMonitor is gated on the Prometheus Operator's, the webhook on
	@# cert-manager's and the whole render on the operator's own, and `helm template` off a
	@# cluster otherwise reports all three as absent and never exercises those branches.
	@for values in $(CHART_VALUES); do \
		echo "==> $$values"; \
		$(HELM) template netbox-operator $(CHART) -n netbox-operator-system \
			-f $$values --api-versions monitoring.coreos.com/v1 \
			--api-versions cert-manager.io/v1 \
			--api-versions $(CRD_API_VERSION) >/dev/null || exit 1; \
	done
	@# And once with cert-manager absent, which is the other half of the webhook gate and the
	@# render a default install on a cluster without cert-manager actually gets: no
	@# Certificate, no configuration, and --enable-webhooks=false on the manager so it comes
	@# up rather than CrashLooping (#249).
	@echo "==> $(CHART)/values.yaml, cert-manager absent"
	@$(HELM) template netbox-operator $(CHART) -n netbox-operator-system \
		-f $(CHART)/values.yaml --api-versions monitoring.coreos.com/v1 \
		--api-versions $(CRD_API_VERSION) >/dev/null
	@# The CRD precondition, asserted rather than assumed. A guard that silently stopped
	@# firing would hand back exactly the failure it exists to replace -- an install that
	@# succeeds and a manager that CrashLoops on "no matches for kind" -- and nothing else
	@# in this repository would notice, because every other render here passes
	@# --api-versions and therefore takes the passing branch.
	@echo "==> $(CHART)/values.yaml, CRDs absent (must fail)"
	@if $(HELM) template netbox-operator $(CHART) -n netbox-operator-system \
		-f $(CHART)/values.yaml >/dev/null 2>&1; then \
		echo "The chart rendered with $(CRD_API_VERSION) absent from Capabilities."; \
		echo "templates/crds-precondition.yaml is not firing, so an install that skipped"; \
		echo "'make install-crds' would succeed and CrashLoop instead (#265)."; \
		exit 1; \
	fi
	@# And the escape hatch that render needs, for anyone templating away from a cluster.
	@echo "==> $(CHART)/values.yaml, CRDs absent and crds.check=false"
	@$(HELM) template netbox-operator $(CHART) -n netbox-operator-system \
		-f $(CHART)/values.yaml --set crds.check=false >/dev/null

# Every RBAC object the chart renders, over both value files. Committed, so an accidental
# widening -- a `secrets` rule that became cluster-scoped, a Role that became a ClusterRole
# -- is a reviewable diff rather than four lines inside a 400-line render (#100).
HELM_GOLDEN ?= $(CHART)/ci/golden-rbac.yaml

.PHONY: helm-golden
helm-golden: ## Regenerate the golden RBAC render.
	@# HELM= is passed explicitly: hack/helm-golden.sh reads it from the *environment*
	@# (helm=${HELM:-helm}), and make does not export a variable it was not told to. Without
	@# this the script silently used whatever helm was on PATH while every other chart target
	@# used the pinned one -- and Helm 4 emits two trailing blank lines per document that
	@# Helm 3 does not, so helm-verify failed on formatting for anyone with Helm 4 installed
	@# and passed in CI, which installs the pin.
	@HELM=$(HELM) ./hack/helm-golden.sh >$(HELM_GOLDEN)

.PHONY: helm-verify
helm-verify: helm-lint helm-template helm-golden ## Fail if the chart's rendered RBAC changed.
	@git diff --exit-code -- $(HELM_GOLDEN) || { \
		echo ""; \
		echo "The chart's rendered RBAC changed. If that is deliberate -- and read the diff"; \
		echo "as a privilege change, not as a formatting one -- run 'make helm-golden' and"; \
		echo "commit the result. See docs/operations/rbac.md."; \
		exit 1; }

# The ceiling the packaged chart may not cross, and the check that stops #265 coming back.
#
# Helm 3 stores the whole chart, the rendered manifest and the values gzipped and
# base64-encoded in one release Secret, and the API server rejects a Secret whose data
# exceeds 1048576 bytes. Bundling the CRDs made the package 424168 bytes, which crossed that
# line and failed every install of this chart; without them it is under 20 KB.
#
# 262144 is deliberately nowhere near either number. It is more than a dozen times the chart
# as it stands, so ordinary growth never trips it, and a fraction of what a returning crds/
# directory or an equivalently large addition would be -- which is the only failure this is
# trying to catch, and the one that grows with the catalogue (NBO-052/053/057/058/068).
CHART_MAX_BYTES ?= 262144

.PHONY: helm-package
helm-package: ## Package the chart into dist/, and fail if it got big enough to break installs.
	@mkdir -p dist
	$(HELM) package $(CHART) --destination dist
	@# wc -c rather than stat: BSD and GNU stat disagree on the flag for a file's size.
	@tgz=dist/netbox-operator-$(CHART_VERSION).tgz; \
	size=$$(wc -c <"$$tgz" | tr -d ' '); \
	echo "$$tgz is $$size bytes (ceiling $(CHART_MAX_BYTES))"; \
	if [ "$$size" -gt "$(CHART_MAX_BYTES)" ]; then \
		echo ""; \
		echo "The packaged chart is over $(CHART_MAX_BYTES) bytes. Helm stores it in the release"; \
		echo "Secret, the API server caps that Secret at 1 MiB, and a chart this size is how"; \
		echo "every 'helm install' came to fail in #265. Ship the bulk out of the chart --"; \
		echo "the CRDs travel as 'make crd-bundle' -- rather than raising this ceiling."; \
		exit 1; \
	fi

##@ CRDs

# The CRDs are not in the chart (#265) and are not installed by Helm. These two targets are
# the supported way in, and both server-side apply: these CRDs are large enough that the
# kubectl.kubernetes.io/last-applied-configuration annotation a client-side apply stores
# inside each object is a problem of its own. --force-conflicts because the field manager
# that owns them changes with the tool somebody last used.
CRD_BUNDLE ?= dist/netbox-operator-crds-$(CHART_VERSION).yaml

.PHONY: install-crds
install-crds: ## Apply the CRDs to the current cluster. Do this before installing the chart.
	@# config/crd/bases rather than `kustomize build config/crd`, so this needs nothing but
	@# kubectl: it is the step a user follows, not part of the dev loop (`make install` is).
	kubectl apply --server-side --force-conflicts -f config/crd/bases/

.PHONY: upgrade-crds
upgrade-crds: install-crds ## Apply the CRDs after a chart upgrade. Same apply as install-crds.
	@# Kept as its own name because installing and upgrading the CRDs are the same command
	@# and two different moments, and docs/install.md and NOTES.txt name the moment.

.PHONY: crd-bundle
crd-bundle: ## Write every CRD into one applyable file in dist/, for release and for URLs.
	@mkdir -p dist
	@./hack/crd-bundle.sh >$(CRD_BUNDLE)
	@echo "$(CRD_BUNDLE) ($$(wc -c <$(CRD_BUNDLE) | tr -d ' ') bytes)"

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

.PHONY: kind
kind: $(KIND) ## Install the pinned kind into ./bin.
$(KIND): $(LOCALBIN)
	$(call go-install-tool,$(KIND),sigs.k8s.io/kind,$(KIND_VERSION))

.PHONY: helm-bin
helm-bin: $(HELM_BIN) ## Install the pinned helm into ./bin, for the e2e suite.
$(HELM_BIN): $(LOCALBIN)
	@# A release tarball rather than `go install helm.sh/helm/v3/cmd/helm`: Helm's own module
	@# pins a toolchain and building it from source is minutes to obtain the binary its
	@# release page already has. The version is pinned like every other tool here.
	@#
	@# `helm` is left on PATH for the chart targets, which CI installs globally. This copy
	@# exists so `make test-e2e` needs nothing installed by hand; the harness prefers ./bin.
	@set -e; \
	os=$$(uname -s | tr '[:upper:]' '[:lower:]'); \
	case "$$(uname -m)" in \
		x86_64|amd64) arch=amd64 ;; \
		aarch64|arm64) arch=arm64 ;; \
		*) echo "unsupported architecture $$(uname -m) for a helm release tarball"; exit 1 ;; \
	esac; \
	tmp=$$(mktemp -d); \
	trap 'rm -rf "$$tmp"' EXIT; \
	echo "Downloading helm $(HELM_VERSION) ($$os/$$arch)"; \
	curl -fsSL "https://get.helm.sh/helm-$(HELM_VERSION)-$$os-$$arch.tar.gz" | tar xz -C "$$tmp"; \
	install -m 0755 "$$tmp/$$os-$$arch/helm" "$(HELM_BIN)"

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
