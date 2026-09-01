#!/usr/bin/env bash

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

# Stands up everything the e2e tests need and runs them.
#
# This script owns the cluster-scoped setup -- the kind cluster, cert-manager, kcp-operator and the
# image under test. The tests own everything below that: each creates a namespace and deploys its
# own kcp into it, so the setup here is done once no matter how many tests run.
#
# kcp-operator is deployed from its main branch by default, which is the point of the exercise: the
# support for custom virtual workspaces with init containers this component depends on lives there,
# and this is what tells us whether the two still fit together. Pin KCP_OPERATOR_REF to a tag to
# test against a release instead.
#
# Environment:
#   KIND_CLUSTER_NAME    name of the kind cluster to create (default: access-vw-e2e)
#   USE_EXISTING_CLUSTER use $KUBECONFIG instead of creating a cluster (default: false)
#   NO_TEARDOWN          keep the cluster and the test namespaces afterwards (default: false)
#   KCP_OPERATOR_REF     git ref of kcp-operator to deploy (default: main)
#   KCP_OPERATOR_IMG     kcp-operator image (default: ghcr.io/kcp-dev/kcp-operator:$KCP_OPERATOR_REF)
#   ACCESS_VW_IMG        image to build and test (default: localhost/access-vw:e2e)
#   SKIP_IMAGE_BUILD     reuse an ACCESS_VW_IMG that is already loaded (default: false)
#   WHAT                 packages to test (default: ./test/e2e/...)
#   TEST_ARGS            extra `go test` arguments (default: -timeout 60m -v)

set -o errexit
set -o nounset
set -o pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${REPO_ROOT}"

KIND_CLUSTER_NAME="${KIND_CLUSTER_NAME:-access-vw-e2e}"
USE_EXISTING_CLUSTER="${USE_EXISTING_CLUSTER:-false}"
NO_TEARDOWN="${NO_TEARDOWN:-false}"
KCP_OPERATOR_REF="${KCP_OPERATOR_REF:-main}"
KCP_OPERATOR_IMG="${KCP_OPERATOR_IMG:-ghcr.io/kcp-dev/kcp-operator:${KCP_OPERATOR_REF}}"
ACCESS_VW_IMG="${ACCESS_VW_IMG:-localhost/access-vw:e2e}"
SKIP_IMAGE_BUILD="${SKIP_IMAGE_BUILD:-false}"
CERT_MANAGER_VERSION="${CERT_MANAGER_VERSION:-v1.20.2}"

DATA_DIR="${REPO_ROOT}/.e2e-${KIND_CLUSTER_NAME}"
mkdir -p "${DATA_DIR}"

for tool in kind kubectl helm docker go git; do
	if ! command -v "${tool}" >/dev/null 2>&1; then
		echo "ERROR: ${tool} is required but not installed." >&2
		exit 1
	fi
done

# dump_diagnostics prints what a failed run needs to be understood after the cluster is gone. The
# tests log the virtual workspace's own pods themselves; what they cannot show is the state of the
# objects kcp-operator was asked to reconcile, or why it did not.
dump_diagnostics() {
	echo
	echo "===== e2e diagnostics ====="

	kubectl get pods --all-namespaces || true
	kubectl get rootshards,frontproxies,virtualworkspaces,kubeconfigs --all-namespaces -o wide || true
	kubectl --namespace kcp-operator-system logs deployment/kcp-operator-controller-manager --tail=300 || true

	echo "===== end diagnostics ====="
	echo
}

teardown() {
	local code=$?

	if [[ ${code} -ne 0 ]]; then
		dump_diagnostics
	fi

	# Never delete a cluster this script did not create.
	if [[ "${USE_EXISTING_CLUSTER}" == "true" ]]; then
		return
	fi

	if [[ "${NO_TEARDOWN}" == "true" ]]; then
		echo "NO_TEARDOWN is set, keeping the kind cluster ${KIND_CLUSTER_NAME} (kubeconfig: ${KUBECONFIG})."

		return
	fi

	echo "Deleting the kind cluster ${KIND_CLUSTER_NAME}..."
	kind delete cluster --name "${KIND_CLUSTER_NAME}" || true
}

# Cluster

if [[ "${USE_EXISTING_CLUSTER}" == "true" ]]; then
	if [[ -z "${KUBECONFIG:-}" ]]; then
		echo "ERROR: KUBECONFIG must be set when USE_EXISTING_CLUSTER=true." >&2
		exit 1
	fi
	export KUBECONFIG="$(realpath "${KUBECONFIG}")"
	echo "Using the existing cluster in ${KUBECONFIG}."

	# Set for the diagnostics on failure; teardown will not touch a cluster it did not create.
	trap teardown EXIT
else
	export KUBECONFIG="${DATA_DIR}/kind.kubeconfig"

	if kind get clusters 2>/dev/null | grep -qx "${KIND_CLUSTER_NAME}"; then
		echo "Reusing the existing kind cluster ${KIND_CLUSTER_NAME}."
		kind export kubeconfig --name "${KIND_CLUSTER_NAME}" --kubeconfig "${KUBECONFIG}"
	else
		echo "Creating the kind cluster ${KIND_CLUSTER_NAME}..."
		kind create cluster --name "${KIND_CLUSTER_NAME}" --kubeconfig "${KUBECONFIG}"
	fi

	chmod 600 "${KUBECONFIG}"

	trap teardown EXIT
fi

echo "Kubeconfig is ${KUBECONFIG}."

# kcp needs more inotify instances than a kind node allows by default. Each shard and front-proxy
# runs several watchers, and the failure mode without this is a pod that starts and then stops
# reacting, which is considerably harder to diagnose than a failure here.
echo "Raising inotify limits on the kind nodes..."
for node in $(kind get nodes --name "${KIND_CLUSTER_NAME}" 2>/dev/null || true); do
	docker exec "${node}" sysctl -w fs.inotify.max_user_watches=1048576 >/dev/null
	docker exec "${node}" sysctl -w fs.inotify.max_user_instances=1024 >/dev/null
done

# cert-manager, which issues every certificate kcp-operator asks for

echo "Deploying cert-manager ${CERT_MANAGER_VERSION}..."
helm repo add jetstack https://charts.jetstack.io --force-update >/dev/null
helm repo update >/dev/null
helm upgrade \
	--install \
	--namespace cert-manager \
	--create-namespace \
	--version "${CERT_MANAGER_VERSION}" \
	--set crds.enabled=true \
	--atomic \
	cert-manager jetstack/cert-manager

kubectl apply --filename - <<'EOF'
apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata:
  name: selfsigned
spec:
  selfSigned: {}
EOF

# kcp-operator

OPERATOR_DIR="${DATA_DIR}/kcp-operator"

if [[ -d "${OPERATOR_DIR}/.git" ]]; then
	echo "Updating the kcp-operator checkout to ${KCP_OPERATOR_REF}..."
	git -C "${OPERATOR_DIR}" fetch --depth 1 origin "${KCP_OPERATOR_REF}"
	git -C "${OPERATOR_DIR}" checkout --quiet FETCH_HEAD
else
	echo "Cloning kcp-operator at ${KCP_OPERATOR_REF}..."
	git clone --quiet --depth 1 --branch "${KCP_OPERATOR_REF}" \
		https://github.com/kcp-dev/kcp-operator "${OPERATOR_DIR}"
fi

echo "kcp-operator is at $(git -C "${OPERATOR_DIR}" rev-parse --short HEAD)."

echo "Deploying kcp-operator (${KCP_OPERATOR_IMG})..."
# config/default already pulls in the CRDs, so this is the whole installation. The manifests come
# from the checkout while the image comes from a registry, so the tag config/manager happens to
# carry is replaced rather than trusted. `kubectl kustomize` avoids needing a separate kustomize
# binary on the machine running this.
kubectl kustomize "${OPERATOR_DIR}/config/default" \
	| sed -E "s|image: ghcr\.io/kcp-dev/kcp-operator:.*|image: ${KCP_OPERATOR_IMG}|" \
	| kubectl apply --server-side --filename -

echo "Waiting for kcp-operator to become available..."
kubectl --namespace kcp-operator-system rollout status deployment/kcp-operator-controller-manager --timeout=5m

# The image under test

if [[ "${SKIP_IMAGE_BUILD}" != "true" ]]; then
	echo "Building ${ACCESS_VW_IMG}..."
	docker build --tag "${ACCESS_VW_IMG}" "${REPO_ROOT}"
fi

if [[ "${USE_EXISTING_CLUSTER}" != "true" ]]; then
	echo "Loading ${ACCESS_VW_IMG} into the kind cluster..."
	kind load docker-image "${ACCESS_VW_IMG}" --name "${KIND_CLUSTER_NAME}"
fi

# Tests

export ACCESS_VW_IMG
export NO_TEARDOWN

WHAT="${WHAT:-./test/e2e/...}"
TEST_ARGS="${TEST_ARGS:--timeout 60m -v}"

echo "Running the e2e tests..."
set -x
# shellcheck disable=SC2086 # TEST_ARGS is a deliberately word-split argument list.
go test -tags e2e ${TEST_ARGS} "${WHAT}"
