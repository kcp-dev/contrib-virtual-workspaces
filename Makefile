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

GO      ?= go
BIN_DIR ?= bin
IMAGE   ?= ghcr.io/kcp-dev/contrib-mcp-vw:latest

# Local development. ACCESS_URL must reach the access virtual workspace, and the
# authentication configuration MUST match kcp's: identity is used both to ask the
# access VW and to impersonate, and both compare usernames verbatim, so a
# mismatch reads as an empty workspace list rather than an auth error.
KUBECONFIG  ?= $(HOME)/.kcp/admin.kubeconfig
ACCESS_URL  ?= https://localhost:9443/services/access
SECURE_PORT ?= 9444
AUTH_CONFIG ?=

.DEFAULT_GOAL := help

.PHONY: help
help: ## Show available targets
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

.PHONY: build
build: ## Build the binary into bin/
	$(GO) build -o $(BIN_DIR)/mcp-virtual-workspace .

.PHONY: test
test: ## Run unit tests
	$(GO) test -race ./...

.PHONY: vet
vet: ## Run go vet
	$(GO) vet ./...

.PHONY: tidy
tidy: ## Sync go.mod / go.sum
	$(GO) mod tidy

.PHONY: verify
verify: vet verify-fork-pin ## Run every verification check
	$(GO) mod tidy -diff

.PHONY: verify-fork-pin
verify-fork-pin: ## Check the kcp Kubernetes fork pin matches virtual-workspace-framework
	./hack/verify-fork-pin.sh

.PHONY: serve
serve: build ## Run against a local kcp and access virtual workspace
	$(BIN_DIR)/mcp-virtual-workspace serve \
		--kubeconfig $(KUBECONFIG) \
		--access-url $(ACCESS_URL) \
		--secure-port $(SECURE_PORT) \
		$(if $(AUTH_CONFIG),--authentication-config $(AUTH_CONFIG),) \
		-v=4

.PHONY: image
image: ## Build the container image
	docker build -t $(IMAGE) .

.PHONY: clean
clean: ## Remove build artifacts
	rm -rf $(BIN_DIR)
