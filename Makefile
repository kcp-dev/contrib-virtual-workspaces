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

GO ?= go
BIN_DIR ?= bin

# Where a kcp checkout lives. kcp is a separate Go module, so it cannot be
# built or `go run` from this one -- "directory ../kcp/cmd/kcp outside main
# module or its selected dependencies" is Go refusing exactly that. Build it
# over there and drop the binary in bin/ so everything runs from this
# directory, next to the pki/ and .local/ it needs.
KCP_DIR ?= ../kcp

.PHONY: all
all: build

.PHONY: build
build: ## Build the server, the endpoint slice controller and the example webhook.
	$(GO) build -o $(BIN_DIR)/ephemeral-virtual-workspace ./cmd/ephemeral-virtual-workspace
	$(GO) build -o $(BIN_DIR)/endpointslice-controller ./cmd/endpointslice-controller
	$(GO) build -o $(BIN_DIR)/example-webhook ./examples/webhook

.PHONY: kcp
kcp: ## Build kcp from $(KCP_DIR) into bin/kcp.
	@test -d $(KCP_DIR) || { echo "no kcp checkout at $(KCP_DIR); set KCP_DIR=/path/to/kcp"; exit 1; }
	$(GO) build -C $(KCP_DIR) -o $(abspath $(BIN_DIR))/kcp ./cmd/kcp
	@echo "built $(BIN_DIR)/kcp from $(KCP_DIR)"

.PHONY: kubectl-ws
kubectl-ws: ## Build the kubectl ws plugin from $(KCP_DIR) into bin/.
	@test -d $(KCP_DIR) || { echo "no kcp checkout at $(KCP_DIR); set KCP_DIR=/path/to/kcp"; exit 1; }
	$(GO) build -C $(KCP_DIR)/staging/src/github.com/kcp-dev/cli -o $(abspath $(BIN_DIR))/kubectl-ws ./cmd/kubectl-ws
	@echo "built $(BIN_DIR)/kubectl-ws; add $(BIN_DIR) to PATH to use 'kubectl ws'"

.PHONY: test
test: ## Run unit tests.
	$(GO) test ./...

.PHONY: verify
verify: verify-gofmt vet ## Run all static checks.

.PHONY: verify-gofmt
verify-gofmt:
	@out=$$(gofmt -l -s $$(find . -name '*.go' -not -path './$(BIN_DIR)/*')); \
	if [ -n "$$out" ]; then echo "not gofmt'd:"; echo "$$out"; exit 1; fi

.PHONY: vet
vet:
	$(GO) vet ./...

.PHONY: tidy
tidy:
	$(GO) mod tidy

.PHONY: clean
clean:
	rm -rf $(BIN_DIR)

.PHONY: help
help:
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-20s\033[0m %s\n", $$1, $$2}'
