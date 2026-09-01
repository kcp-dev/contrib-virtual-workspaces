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

# Unit tests, vet, tidy, verification and images live in the repository root
# Makefile (`make -C .. help`); the targets here are local development only.

GO      ?= go
BIN_DIR ?= bin

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

.PHONY: test-e2e
test-e2e: ## Deploy into a throwaway kind cluster via kcp-operator and run the e2e tests
	./hack/ci/run-e2e-tests.sh

.PHONY: test-e2e-keep
test-e2e-keep: ## Same, but keep the cluster and namespaces afterwards for inspection
	NO_TEARDOWN=true ./hack/ci/run-e2e-tests.sh

.PHONY: serve
serve: build ## Run against a local kcp and access virtual workspace
	$(BIN_DIR)/mcp-virtual-workspace serve \
		--kubeconfig $(KUBECONFIG) \
		--access-url $(ACCESS_URL) \
		--secure-port $(SECURE_PORT) \
		$(if $(AUTH_CONFIG),--authentication-config $(AUTH_CONFIG),) \
		-v=4

.PHONY: clean
clean: ## Remove build artifacts
	rm -rf $(BIN_DIR)
