# Default values assume a local kcp launched with `kcp start` from
# this checkout. Override on the command line, e.g.
#   make run-access-vw KUBECONFIG=/path/to/admin.kubeconfig

KUBECONFIG       ?= $(HOME)/.kcp/admin.kubeconfig
ENDPOINT_BASE    ?= https://localhost:6443/clusters/
SECURE_PORT      ?= 9443
APIEXPORT_SLICE  ?= access.contrib.kcp.io
WORKSPACE_PREFIX      ?= root:access
CONTROLLERS_WORKSPACE ?= controllers

# Shorthand for the workspace the targets below operate in. Deliberately NOT
# overridable: overriding it alone would point the demo targets at a workspace
# `init` never installed into.
EXPORT_PATH = $(WORKSPACE_PREFIX):$(CONTROLLERS_WORKSPACE)
WS_ALICE         ?= workspace-alice
WS_BOB           ?= workspace-bob

SCAR_URL = https://localhost:$(SECURE_PORT)/services/access/apis/access.contrib.kcp.io/v1alpha1/selfclusteraccessreviews

# Bearer token for smoke tests; defaults to the kcp-admin token from the
# local kcp admin kubeconfig. Override TOKEN for other identities.
TOKEN ?= $(shell kubectl --kubeconfig $(KUBECONFIG) config view --raw \
	-o jsonpath='{.users[?(@.name=="kcp-admin")].user.token}' 2>/dev/null)

.PHONY: help
help: ## Show available targets
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "  \033[36m%-24s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

#Build & test

.PHONY: build
build: ## Build all binaries into bin/
	go build -o bin/access-vw ./cmd/access-vw
	go build -o bin/access-vw-init ./cmd/init
	go build -o bin/scar-to-kubeconfig ./cmd/scar-to-kubeconfig

.PHONY: test
test: ## Run unit tests
	go test ./...

.PHONY: test-e2e
test-e2e: ## Deploy into a throwaway kind cluster via kcp-operator and run the e2e tests
	./hack/ci/run-e2e-tests.sh

.PHONY: test-e2e-keep
test-e2e-keep: ## Same, but keep the cluster and namespaces afterwards for inspection
	NO_TEARDOWN=true ./hack/ci/run-e2e-tests.sh

.PHONY: vet
vet: ## Run go vet
	go vet ./...
	go vet -tags e2e ./test/...

.PHONY: tidy
tidy: ## Sync go.mod / go.sum
	go mod tidy

.PHONY: verify
verify: vet verify-fork-pin verify-e2e-manifests ## Run all verification checks
	go mod tidy -diff

.PHONY: verify-e2e-manifests
verify-e2e-manifests: ## Render the e2e manifests without a cluster, so template errors fail here
	go test -tags e2e -run TestManifestsRender -count=1 ./test/e2e/

.PHONY: verify-fork-pin
verify-fork-pin: ## Check the kcp Kubernetes fork pin matches virtual-workspace-framework
	./hack/verify-fork-pin.sh

.PHONY: clean
clean: ## Remove build artifacts
	rm -rf bin/

#kcp setup
# These operate on $(EXPORT_PATH), which `init` creates if it does not exist.

.PHONY: init
init: build ## Bootstrap kcp: create the workspace, install the APIExport, schema, bind RBAC and endpoint slice, then verify
	./bin/access-vw-init \
		--kubeconfig $(KUBECONFIG) \
		--workspace-prefix $(WORKSPACE_PREFIX) \
		--controllers-workspace $(CONTROLLERS_WORKSPACE)


.PHONY: show-apiexport
show-apiexport: ## Show the APIExport, ARS and generated EndpointSlice
	kubectl ws use $(EXPORT_PATH)
	kubectl get apiexports.apis.kcp.io
	kubectl get apiresourceschemas.apis.kcp.io
	kubectl get apiexportendpointslices.apis.kcp.io 2>/dev/null || true

#Test workspace setup

.PHONY: create-test-workspaces
create-test-workspaces: ## Create workspace-alice and workspace-bob, bind APIExport in each
	kubectl ws use $(EXPORT_PATH)
	-kubectl ws create $(WS_ALICE)
	kubectl ws use $(WS_ALICE)
	kubectl apply -f config/examples/apibinding-consumer.yaml
	kubectl ws use $(EXPORT_PATH)
	-kubectl ws create $(WS_BOB)
	kubectl ws use $(WS_BOB)
	kubectl apply -f config/examples/apibinding-consumer.yaml
	kubectl ws use $(EXPORT_PATH)

.PHONY: seed-rbac
seed-rbac: ## Seed RBAC: alice-sa in workspace-alice, bob-sa in workspace-bob
	kubectl ws use $(EXPORT_PATH):$(WS_ALICE)
	kubectl apply -f config/rbac/seed-rbac-alice.yaml
	kubectl ws use $(EXPORT_PATH):$(WS_BOB)
	kubectl apply -f config/rbac/seed-rbac-bob.yaml
	kubectl ws use $(EXPORT_PATH)


.PHONY: cleanup
cleanup: ## Remove all test resources: RBAC, workspaces, APIExport
	-kubectl ws use $(EXPORT_PATH):$(WS_ALICE) && kubectl delete -f config/rbac/seed-rbac-alice.yaml
	-kubectl ws use $(EXPORT_PATH):$(WS_BOB) && kubectl delete -f config/rbac/seed-rbac-bob.yaml
	-kubectl ws use $(EXPORT_PATH) && kubectl ws delete $(WS_ALICE)
	-kubectl ws use $(EXPORT_PATH) && kubectl ws delete $(WS_BOB)
	-kubectl ws use $(EXPORT_PATH) && kubectl delete \
		-f config/apiexport/apiexportendpointslice.yaml \
		-f config/apiexport/rbac-bind.yaml \
		-f config/apiexport/apiexport.yaml \
		-f config/apiexport/apiresourceschema.yaml
	kubectl ws use $(EXPORT_PATH)

# ── Run against kcp ──────────────────────────────────────────────────

.PHONY: run-access-vw
run-access-vw: build ## Run against kcp (TLS on $(SECURE_PORT), bearer tokens via TokenReview; self-signs a dev cert)
	./bin/access-vw \
		--secure-port $(SECURE_PORT) \
		--kubeconfig $(KUBECONFIG) \
		--apiexport-endpointslice $(APIEXPORT_SLICE) \
		--endpoint-base $(ENDPOINT_BASE)

#Smoke tests
# The server validates bearer tokens via TokenReview against kcp;
# identity comes from the token (no header spoofing). Override TOKEN
# to test other identities (e.g. a ServiceAccount token).

.PHONY: scar
scar: ## Issue a SCAR as the caller identified by $(TOKEN)
	@curl -ksf -X POST \
		-H "Authorization: Bearer $(TOKEN)" \
		-H 'Content-Type: application/json' -d '{}' \
		$(SCAR_URL) | jq

.PHONY: debug-graph
debug-graph: ## Dump the access graph (authenticated)
	@curl -ksf -H "Authorization: Bearer $(TOKEN)" \
		https://localhost:$(SECURE_PORT)/debug/graph | jq

.PHONY: healthz
healthz: ## Hit /healthz
	@curl -ksf https://localhost:$(SECURE_PORT)/healthz; echo

#MCP demo

.PHONY: mcp-demo
mcp-demo: build ## Generate scoped alice.kubeconfig for MCP demo
	@echo "==> Generating alice.kubeconfig (workspace-alice only)..."; \
	kubectl ws use $(EXPORT_PATH):$(WS_ALICE) >/dev/null 2>&1 || true; \
	TOKEN_VAL=$$(kubectl create token alice-sa --namespace=default --duration=1h 2>/dev/null); \
	if [ -z "$$TOKEN_VAL" ]; then \
		echo "error: could not obtain token for alice-sa. Run 'make seed-rbac' first." >&2; \
		exit 1; \
	fi; \
	./bin/scar-to-kubeconfig -scar-url "$(SCAR_URL)" -token "$$TOKEN_VAL" -insecure -output alice.kubeconfig
	@kubectl ws use $(EXPORT_PATH) >/dev/null 2>&1 || true
	@echo ""
	@echo "Kubeconfig ready:"
	@echo "  alice.kubeconfig → can access workspace-alice (create/list workspaces, view resources)"
	@echo ""
	@echo "Run MCP server:"
	@echo "  kubernetes-mcp-server --kubeconfig=$(CURDIR)/alice.kubeconfig --cluster-provider=kcp --toolsets=core,kcp --port 8080"
	@echo ""
	@echo "Or add to ~/.copilot/mcp-config.json:"
	@echo '  {"mcpServers":{"kcp-access":{"type":"local","command":"kubernetes-mcp-server","args":["--kubeconfig","$(CURDIR)/alice.kubeconfig","--cluster-provider=kcp","--toolsets=core,kcp"]}}}'

#Container image
# For a cluster-based run, prefer `make test-e2e`: it builds this image, loads it into kind and
# deploys it through kcp-operator the way a user would.

.PHONY: docker-build
docker-build: ## Build access-vw Docker image
	docker build -t localhost/access-vw:local .
