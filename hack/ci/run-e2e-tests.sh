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

# Stands the stack up and runs the e2e tests against it.
#
# The split is the same one hack/local-up.sh uses and the same one the tests
# expect: this script owns processes, certificates and ports; the Go tests own
# workspaces, objects and assertions. Nothing here creates an APIExport or a
# binding -- that is what is being tested.
#
# It deliberately does NOT deploy through kind and kcp-operator, the way the
# sibling access virtual workspace does. That approach needs a published kcp
# image, and this component depends on kcp changes that are in no release: the
# kcp under test has to be built from a checkout. Once those changes land, this
# is the piece to reconsider.
#
#   hack/ci/run-e2e-tests.sh              run everything, then tear it down
#   NO_TEARDOWN=true hack/ci/...          leave it running for inspection
#   WHAT=./test/e2e/... hack/ci/...       narrow what is run
#   TEST_ARGS="-run Identity -v" hack/...

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${REPO_ROOT}"

KCP_DIR="${KCP_DIR:-$(cd "${REPO_ROOT}/.." && pwd)/kcp}"
KCP_BIN="${KCP_BIN:-${REPO_ROOT}/bin/kcp}"

WORK_DIR="${WORK_DIR:-${REPO_ROOT}/.e2e}"
PKI_DIR="${WORK_DIR}/pki"
LOG_DIR="${WORK_DIR}/logs"
RUN_DIR="${WORK_DIR}/run"
KCP_ROOT_DIR="${WORK_DIR}/kcp"

KCP_PORT="${KCP_PORT:-6443}"
VW_PORT="${VW_PORT:-6454}"
WEBHOOK_PORT="${WEBHOOK_PORT:-18443}"
ETCD_CLIENT_PORT="${ETCD_CLIENT_PORT:-2379}"
ETCD_PEER_PORT="${ETCD_PEER_PORT:-2380}"

VW_NAME="${VW_NAME:-ephemeral-buckets}"
SHARD_USER="${SHARD_USER:-kcp-shard}"

# Must match what the tests create and what pki/ephemeral-config.yaml names.
PROVIDER_WS="root:providers:s3"
EXPORT_NAME="s3.example.com"

NO_TEARDOWN="${NO_TEARDOWN:-false}"
SKIP_BUILD="${SKIP_BUILD:-false}"

log() { printf '\033[1;32m==>\033[0m %s\n' "$*"; }
die() { printf '\033[1;31mERR\033[0m %s\n' "$*" >&2; exit 1; }

### diagnostics and teardown ##################################################

dump_diagnostics() {
  echo
  echo "===== e2e diagnostics ====="

  for name in kcp vw webhook endpointslice; do
    if [[ -f "${LOG_DIR}/${name}.log" ]]; then
      echo "----- ${name} (last 100 lines) -----"
      tail -n 100 "${LOG_DIR}/${name}.log" || true
    fi
  done

  # The one line that explains most failures: the shard could not resolve the
  # endpoint slice, so the resource never became servable.
  echo "----- endpoint slice resolution -----"
  grep -F "failed to retrieve virtual workspace URL" "${LOG_DIR}/kcp.log" | tail -n 5 || echo "(none, which is good)"

  echo "===== end diagnostics ====="
  echo
}

stop_all() {
  [[ -d "${RUN_DIR}" ]] || return 0

  for pidfile in "${RUN_DIR}"/*.pid; do
    [[ -f "${pidfile}" ]] || continue
    local name pid
    name="$(basename "${pidfile}" .pid)"
    pid="$(cat "${pidfile}")"
    if kill -0 "${pid}" 2>/dev/null; then
      log "stopping ${name} (pid ${pid})"
      kill "${pid}" 2>/dev/null || true
      wait "${pid}" 2>/dev/null || true
    fi
    rm -f "${pidfile}"
  done
}

teardown() {
  local code=$?

  if [[ ${code} -ne 0 ]]; then
    dump_diagnostics
  fi

  if [[ "${NO_TEARDOWN}" == "true" ]]; then
    log "NO_TEARDOWN is set, leaving everything running (KUBECONFIG=${KCP_ROOT_DIR}/admin.kubeconfig)"
    return
  fi

  stop_all
}

start_bg() { # name, then command
  local name=$1; shift
  mkdir -p "${LOG_DIR}" "${RUN_DIR}"
  "$@" >"${LOG_DIR}/${name}.log" 2>&1 &
  echo $! >"${RUN_DIR}/${name}.pid"
  log "started ${name} (pid $(cat "${RUN_DIR}/${name}.pid")), logging to ${LOG_DIR}/${name}.log"
}

require_alive() { # name
  local pidfile="${RUN_DIR}/$1.pid"
  [[ -f "${pidfile}" ]] || die "$1 was never started"
  kill -0 "$(cat "${pidfile}")" 2>/dev/null || die "$1 died, see ${LOG_DIR}/$1.log"
}

### preflight #################################################################

for tool in go kubectl curl openssl; do
  command -v "${tool}" >/dev/null 2>&1 || die "${tool} is required but not installed"
done

mkdir -p "${WORK_DIR}" "${LOG_DIR}" "${RUN_DIR}" "${PKI_DIR}"
stop_all
trap teardown EXIT

for port_and_name in "${KCP_PORT}:kcp" "${VW_PORT}:virtual workspace" "${WEBHOOK_PORT}:webhook" \
                     "${ETCD_CLIENT_PORT}:etcd client" "${ETCD_PEER_PORT}:etcd peer"; do
  port=${port_and_name%%:*}
  if lsof -nP -iTCP:"${port}" -sTCP:LISTEN >/dev/null 2>&1; then
    lsof -nP -iTCP:"${port}" -sTCP:LISTEN 2>/dev/null | tail -n +2 >&2
    die "port ${port} (${port_and_name#*:}) is already in use"
  fi
done

if [[ "${SKIP_BUILD}" != "true" ]]; then
  log "building this repository"
  make --no-print-directory build

  # kcp is a separate module, and the e2e needs one carrying the changes this
  # component depends on -- reference-driven replication, the endpoint shards
  # selector, and identity forwarding. None of them are released, so the binary
  # has to come from a checkout.
  if [[ ! -x "${KCP_BIN}" ]]; then
    [[ -d "${KCP_DIR}" ]] || die "no kcp checkout at ${KCP_DIR}; set KCP_DIR=/path/to/kcp"
    log "building kcp from ${KCP_DIR}"
    make --no-print-directory kcp KCP_DIR="${KCP_DIR}"
  fi
fi

[[ -x "${KCP_BIN}" ]] || die "no kcp binary at ${KCP_BIN}"

### PKI, first pass ###########################################################

# Certificates before kcp, because kcp is started with the CA on its command
# line -- and the kubeconfig after it, because that one is derived from kcp's
# own admin.kubeconfig, which does not exist until it has started once.
#
# That two-pass shape is an artifact of `kcp start` being its own certificate
# authority. A deployment provisions its PKI before anything runs and never
# sees it. hack/gen-pki.sh says the same thing at more length.
log "generating certificates"
"${REPO_ROOT}/hack/gen-pki.sh" \
  --kcp-root "${KCP_ROOT_DIR}" \
  --out "${PKI_DIR}" \
  --shard-user "${SHARD_USER}" \
  --webhook-url "https://localhost:${WEBHOOK_PORT}/ephemeral/bucketinfos" \
  --certs-only \
  >"${LOG_DIR}/gen-pki.log" 2>&1 \
  || { cat "${LOG_DIR}/gen-pki.log" >&2; die "hack/gen-pki.sh failed"; }

### kcp #######################################################################

log "starting kcp on :${KCP_PORT}"
start_bg kcp "${KCP_BIN}" start \
  --root-directory="${KCP_ROOT_DIR}" \
  --secure-port="${KCP_PORT}" \
  --embedded-etcd-client-port="${ETCD_CLIENT_PORT}" \
  --embedded-etcd-peer-port="${ETCD_PEER_PORT}" \
  `# Reference-driven replication is gated on this, and it is alpha and off by` \
  `# default. Without it the endpoint slice never reaches the cache server and` \
  `# the resource never becomes servable.` \
  --feature-gates=CacheAPIs=true \
  --shard-virtual-workspace-ca-file="${PKI_DIR}/ca.crt" \
  --shard-client-cert-file="${PKI_DIR}/shard-client.crt" \
  --shard-client-key-file="${PKI_DIR}/shard-client.key" \
  --v="${KCP_V:-2}"

KUBECONFIG_PATH="${KCP_ROOT_DIR}/admin.kubeconfig"

log "waiting for kcp to be ready"
ready=false
for _ in $(seq 1 180); do
  require_alive kcp
  if [[ -f "${KUBECONFIG_PATH}" ]] && \
     kubectl --kubeconfig="${KUBECONFIG_PATH}" get --raw /readyz >/dev/null 2>&1; then
    ready=true
    break
  fi
  sleep 1
done
[[ "${ready}" == true ]] || die "kcp did not become ready, see ${LOG_DIR}/kcp.log"
log "kcp is ready"

### PKI, second pass ##########################################################

# Now that admin.kubeconfig exists, this writes the kubeconfig the virtual
# workspace authenticates to kcp with, and extracts kcp's serving CA.
log "writing the virtual workspace's kubeconfig"
"${REPO_ROOT}/hack/gen-pki.sh" \
  --kcp-root "${KCP_ROOT_DIR}" \
  --out "${PKI_DIR}" \
  --shard-user "${SHARD_USER}" \
  --webhook-url "https://localhost:${WEBHOOK_PORT}/ephemeral/bucketinfos" \
  >>"${LOG_DIR}/gen-pki.log" 2>&1 \
  || { cat "${LOG_DIR}/gen-pki.log" >&2; die "hack/gen-pki.sh failed"; }

[[ -f "${PKI_DIR}/vw-client.kubeconfig" ]] || die "gen-pki.sh did not write vw-client.kubeconfig"

CONFIG_FILE="${PKI_DIR}/ephemeral-config.yaml"
grep -q "path: ${PROVIDER_WS}$" "${CONFIG_FILE}" && grep -q "name: ${EXPORT_NAME}$" "${CONFIG_FILE}" \
  || die "${CONFIG_FILE} does not name ${PROVIDER_WS}|${EXPORT_NAME}; docs/example has drifted from the tests"

### webhook ###################################################################

log "starting the example webhook on :${WEBHOOK_PORT}"
start_bg webhook "${REPO_ROOT}/bin/example-webhook" \
  --address=":${WEBHOOK_PORT}" \
  --tls-cert-file="${PKI_DIR}/webhook.crt" \
  --tls-private-key-file="${PKI_DIR}/webhook.key"

for _ in $(seq 1 30); do
  require_alive webhook
  curl -sf --cacert "${PKI_DIR}/ca.crt" "https://localhost:${WEBHOOK_PORT}/healthz" >/dev/null && break
  sleep 1
done

### virtual workspace #########################################################

log "starting the ephemeral virtual workspace on :${VW_PORT}"
start_bg vw "${REPO_ROOT}/bin/ephemeral-virtual-workspace" \
  --ephemeral-config="${CONFIG_FILE}" \
  --secure-port="${VW_PORT}" \
  --tls-cert-file="${PKI_DIR}/vw.crt" \
  --tls-private-key-file="${PKI_DIR}/vw.key" \
  --client-ca-file="${PKI_DIR}/ca.crt" \
  `# Believe the caller the shard forwards, but only over a connection this CA` \
  `# signed and only from the shard. Without these the request authenticates as` \
  `# the shard's certificate and the Identity subtest is what notices.` \
  --requestheader-client-ca-file="${PKI_DIR}/ca.crt" \
  --requestheader-allowed-names="${SHARD_USER}" \
  --requestheader-username-headers=X-Remote-User \
  --requestheader-group-headers=X-Remote-Group \
  --requestheader-extra-headers-prefix=X-Remote-Extra- \
  --kubeconfig="${PKI_DIR}/vw-client.kubeconfig" \
  --cache-kubeconfig="${PKI_DIR}/vw-client.kubeconfig" \
  --authentication-kubeconfig="${PKI_DIR}/vw-client.kubeconfig" \
  --authentication-skip-lookup \
  --virtual-workspace-name="${VW_NAME}" \
  --allow-private-webhook-addresses \
  --v=4

ready=false
for _ in $(seq 1 60); do
  require_alive vw
  if curl -sf --cacert "${PKI_DIR}/ca.crt" "https://localhost:${VW_PORT}/readyz" >/dev/null; then
    ready=true
    break
  fi
  sleep 1
done
[[ "${ready}" == true ]] || die "the virtual workspace did not become ready, see ${LOG_DIR}/vw.log"
log "virtual workspace is ready"

### endpoint slice controller #################################################

# Started before the workspace it publishes into exists, because the tests are
# what create it. The controller lists on a timer and treats a missing workspace
# or a missing CRD as "not yet", so starting it early costs a few log lines and
# removes an ordering dependency between this script and the tests.
KCP_SERVER="https://localhost:${KCP_PORT}"
KCP_TOKEN="$(kubectl --kubeconfig="${KUBECONFIG_PATH}" config view --raw -o jsonpath='{.users[0].user.token}')"
[[ -n "${KCP_TOKEN}" ]] || die "could not read an admin token out of ${KUBECONFIG_PATH}"

PROVIDER_KUBECONFIG="${PKI_DIR}/provider.kubeconfig"
cat >"${PROVIDER_KUBECONFIG}" <<EOF
apiVersion: v1
kind: Config
clusters:
- name: provider
  cluster:
    server: ${KCP_SERVER}/clusters/${PROVIDER_WS}
    certificate-authority: ${PKI_DIR}/kcp-ca.crt
users:
- name: provider
  user:
    token: ${KCP_TOKEN}
contexts:
- name: provider
  context:
    cluster: provider
    user: provider
current-context: provider
EOF
chmod 600 "${PROVIDER_KUBECONFIG}"

log "starting the endpoint slice controller against ${PROVIDER_WS}"
start_bg endpointslice "${REPO_ROOT}/bin/endpointslice-controller" \
  --kubeconfig="${PROVIDER_KUBECONFIG}" \
  --external-url="https://localhost:${VW_PORT}" \
  --virtual-workspace-name="${VW_NAME}" \
  --export="${EXPORT_NAME}" \
  --v=2

### tests #####################################################################

export KUBECONFIG="${KUBECONFIG_PATH}"
export NO_TEARDOWN

WHAT="${WHAT:-./test/e2e/...}"
TEST_ARGS="${TEST_ARGS:--timeout 30m -v}"

log "running the e2e tests"
set -x
# shellcheck disable=SC2086 # TEST_ARGS is a deliberately word-split argument list.
go test -tags e2e -count=1 ${TEST_ARGS} "${WHAT}"
