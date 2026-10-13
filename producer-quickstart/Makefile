GO      ?= go
BIN_DIR ?= bin
IMAGE   ?= ghcr.io/example/dbaas-producer
TAG     ?= latest

# Where to publish to and run against. Any kcp admin kubeconfig; the Tilt
# stack in contrib-virtual-workspaces writes .tilt/tilt-contrib.kubeconfig.
KUBECONFIG     ?=
WORKSPACE_PATH ?= root:marketplace:producers:dbaas

.PHONY: help
help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'

.PHONY: build
build: ## Build the controller into bin/producer
	$(GO) build -o $(BIN_DIR)/producer ./cmd/producer

.PHONY: test
test: ## Unit tests (no cluster needed)
	$(GO) test ./...

.PHONY: generate
generate: ## Regenerate deepcopy and the APIResourceSchema from apis/
	./hack/update-codegen.sh

.PHONY: verify
verify: ## What CI runs: formatting, vet, build, tests, and that generate is committed
	@out=$$(gofmt -l -s $$(find . -name '*.go' -not -path './$(BIN_DIR)/*')); \
	if [ -n "$$out" ]; then echo "not gofmt'd:"; echo "$$out"; exit 1; fi
	$(GO) vet ./...
	$(GO) mod tidy -diff
	$(MAKE) build test generate
	git diff --exit-code -- apis config

.PHONY: image
image: ## Build the container image
	docker build -t $(IMAGE):$(TAG) .

.PHONY: publish
publish: ## Register the producer and publish the export (KUBECONFIG=<admin kubeconfig>)
	KUBECONFIG=$(KUBECONFIG) ./hack/publish.sh

.PHONY: run
run: build ## Run the controller locally against the producer workspace
	$(BIN_DIR)/producer --kubeconfig $(KUBECONFIG) --workspace-path $(WORKSPACE_PATH)
