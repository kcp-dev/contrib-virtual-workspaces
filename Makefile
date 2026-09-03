# Copyright 2026 The kcp Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# Shared entry points for all three virtual workspaces. Component-specific
# development targets (demos, local serving, kcp helpers) live in the
# Makefile of each component directory: access/, mcp/, ephemeral/.

GO      ?= go
BIN_DIR ?= bin

.DEFAULT_GOAL := help

.PHONY: help
help: ## Show available targets
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z0-9_-]+:.*?## / {printf "  \033[36m%-24s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

# ── Build ────────────────────────────────────────────────────────────

.PHONY: build
build: build-access build-mcp build-ephemeral ## Build every binary into bin/

.PHONY: build-access
build-access: ## Build the access virtual workspace binaries
	$(GO) build -o $(BIN_DIR)/access-vw ./access/cmd/access-vw
	$(GO) build -o $(BIN_DIR)/access-vw-init ./access/cmd/init
	$(GO) build -o $(BIN_DIR)/scar-to-kubeconfig ./access/cmd/scar-to-kubeconfig

.PHONY: build-mcp
build-mcp: ## Build the MCP virtual workspace binary
	$(GO) build -o $(BIN_DIR)/mcp-virtual-workspace ./mcp

.PHONY: build-ephemeral
build-ephemeral: ## Build the ephemeral resources virtual workspace binaries
	$(GO) build -o $(BIN_DIR)/ephemeral-virtual-workspace ./ephemeral/cmd/ephemeral-virtual-workspace
	$(GO) build -o $(BIN_DIR)/endpointslice-controller ./ephemeral/cmd/endpointslice-controller
	$(GO) build -o $(BIN_DIR)/example-webhook ./ephemeral/examples/webhook

# ── Test & verify ────────────────────────────────────────────────────

.PHONY: test
test: ## Run unit tests
	$(GO) test -race $$($(GO) list ./... | grep -v /test/e2e)

GOLANGCI_LINT ?= golangci-lint

.PHONY: lint
lint: ## Run golangci-lint (configuration in .golangci.yml)
	@command -v $(GOLANGCI_LINT) >/dev/null 2>&1 || { \
		echo "golangci-lint not found; install it from https://golangci-lint.run/usage/install/"; exit 1; }
	$(GOLANGCI_LINT) run --timeout 10m ./...

.PHONY: vet
vet: ## Run go vet, including the e2e build tag so the tests cannot rot
	$(GO) vet ./...
	$(GO) vet -tags e2e ./test/... ./access/test/... ./mcp/test/... ./ephemeral/test/...

.PHONY: tidy
tidy: ## Sync go.mod / go.sum
	$(GO) mod tidy

.PHONY: verify
verify: verify-gofmt vet verify-fork-pin verify-e2e-manifests ## Run every verification check
	$(GO) mod tidy -diff

.PHONY: verify-gofmt
verify-gofmt: ## Check gofmt -s cleanliness
	@out=$$(gofmt -l -s $$(find . -name '*.go' -not -path './$(BIN_DIR)/*')); \
	if [ -n "$$out" ]; then echo "not gofmt'd:"; echo "$$out"; exit 1; fi

.PHONY: verify-e2e-manifests
verify-e2e-manifests: ## Render the e2e manifests without a cluster, so template errors fail here
	$(GO) test -tags e2e -run TestManifestsRender -count=1 ./access/test/e2e/ ./mcp/test/e2e/

.PHONY: verify-fork-pin
verify-fork-pin: ## Check the kcp Kubernetes fork pin matches virtual-workspace-framework
	./hack/verify-fork-pin.sh

# ── e2e ──────────────────────────────────────────────────────────────
# Each component owns its e2e harness; these are the entry points CI uses.

.PHONY: test-e2e
test-e2e: test-e2e-access test-e2e-mcp test-e2e-ephemeral ## Run every component's e2e tests in sequence

.PHONY: test-e2e-access
test-e2e-access: ## Run the access VW e2e tests (throwaway kind cluster via kcp-operator)
	./access/hack/ci/run-e2e-tests.sh

.PHONY: test-e2e-mcp
test-e2e-mcp: ## Run the MCP VW e2e tests (kind cluster with access VW alongside)
	./mcp/hack/ci/run-e2e-tests.sh

.PHONY: test-e2e-ephemeral
test-e2e-ephemeral: ## Run the ephemeral VW e2e tests (local processes against a real kcp)
	cd ephemeral && ./hack/ci/run-e2e-tests.sh

# ── Images ───────────────────────────────────────────────────────────
# One Dockerfile, one shared builder stage, three image targets.

IMAGE_PREFIX ?= ghcr.io/kcp-dev/contrib-virtual-workspaces
IMAGE_TAG    ?= latest

.PHONY: images
images: image-access image-mcp image-ephemeral ## Build all three container images

.PHONY: image-access
image-access: ## Build the access-vw container image
	docker build --target access-vw -t $(IMAGE_PREFIX)/access-vw:$(IMAGE_TAG) .

.PHONY: image-mcp
image-mcp: ## Build the mcp-vw container image
	docker build --target mcp-vw -t $(IMAGE_PREFIX)/mcp-vw:$(IMAGE_TAG) .

.PHONY: image-ephemeral
image-ephemeral: ## Build the ephemeral-vw container image
	docker build --target ephemeral-vw -t $(IMAGE_PREFIX)/ephemeral-vw:$(IMAGE_TAG) .

.PHONY: clean
clean: ## Remove build artifacts
	rm -rf $(BIN_DIR)
