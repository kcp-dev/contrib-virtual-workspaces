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

# Stands the marketplace stack up locally and runs the e2e tests against
# it: a real kcp, `marketplace-vw init` (twice, so idempotency is exercised
# on every run), the operator, and the virtual workspace. The tests then
# register providers, publish offerings, enable them from consumer
# workspaces and assert on what materializes — and on what must not.
#
#   hack/ci/run-e2e-tests.sh                  run everything, then tear it down
#   KCP_DIR=/path/to/kcp hack/ci/...          build kcp from a checkout
#   KCP_IMAGE=ghcr.io/kcp-dev/kcp:main ...    take kcp from an image (Linux only)
#   NO_TEARDOWN=true hack/ci/...              leave it running for inspection
#   WHAT=./test/e2e/... TEST_ARGS="-run VW -v" ...

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${REPO_ROOT}"

MONOREPO_ROOT="$(cd "${REPO_ROOT}/.." && pwd)"
KCP_DIR="${KCP_DIR:-$(cd "${MONOREPO_ROOT}/.." && pwd)/kcp}"
KCP_BIN="${KCP_BIN:-${REPO_ROOT}/bin/kcp}"
KCP_IMAGE="${KCP_IMAGE:-}"

WORK_DIR="${WORK_DIR:-${REPO_ROOT}/.e2e}"
PKI_DIR="${WORK_DIR}/pki"
LOG_DIR="${WORK_DIR}/logs"
RUN_DIR="${WORK_DIR}/run"
KCP_ROOT_DIR="${WORK_DIR}/kcp"

KCP_PORT="${KCP_PORT:-6443}"
VW_PORT="${VW_PORT:-6457}"
ETCD_CLIENT_PORT="${ETCD_CLIENT_PORT:-2379}"
ETCD_PEER_PORT="${ETCD_PEER_PORT:-2380}"

NO_TEARDOWN="${NO_TEARDOWN:-false}"
SKIP_BUILD="${SKIP_BUILD:-false}"

log() { printf '\033[1;32m==>\033[0m %s\n' "$*"; }
die() { printf '\033[1;31mERR\033[0m %s\n' "$*" >&2; exit 1; }

### diagnostics and teardown ##################################################

dump_diagnostics() {
  echo
  echo "===== e2e diagnostics ====="
  for name in kcp init operator vw; do
    if [[ -f "${LOG_DIR}/${name}.log" ]]; then
      echo "----- ${name} (last 80 lines) -----"
      tail -n 80 "${LOG_DIR}/${name}.log" || true
    fi
  done
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
      # These pids may be from an earlier invocation, so poll rather than
      # `wait`, and make sure ports are really free.
      for _ in $(seq 1 30); do
        kill -0 "${pid}" 2>/dev/null || break
        sleep 1
      done
      if kill -0 "${pid}" 2>/dev/null; then
        log "killing ${name} (pid ${pid}) forcefully"
        kill -9 "${pid}" 2>/dev/null || true
        sleep 1
      fi
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

for tool in go curl openssl kubectl; do
  command -v "${tool}" >/dev/null 2>&1 || die "${tool} is required but not installed"
done

mkdir -p "${WORK_DIR}" "${LOG_DIR}" "${RUN_DIR}" "${PKI_DIR}"
stop_all
# Every run gets a fresh kcp: a leftover root directory makes kcp replay
# the previous teardown before becoming ready. The PKI is reusable.
rm -rf "${KCP_ROOT_DIR}"
trap teardown EXIT

for port_and_name in "${KCP_PORT}:kcp" "${VW_PORT}:virtual workspace" \
                     "${ETCD_CLIENT_PORT}:etcd client" "${ETCD_PEER_PORT}:etcd peer"; do
  port=${port_and_name%%:*}
  if lsof -nP -iTCP:"${port}" -sTCP:LISTEN >/dev/null 2>&1; then
    lsof -nP -iTCP:"${port}" -sTCP:LISTEN 2>/dev/null | tail -n +2 >&2
    die "port ${port} (${port_and_name#*:}) is already in use"
  fi
done

if [[ "${SKIP_BUILD}" != "true" ]]; then
  log "building the marketplace binary"
  make --no-print-directory -C "${MONOREPO_ROOT}" build-marketplace BIN_DIR="${REPO_ROOT}/bin"

  if [[ ! -x "${KCP_BIN}" ]]; then
    if [[ -n "${KCP_IMAGE}" ]]; then
      [[ "$(uname -s)" == "Linux" ]] || die "KCP_IMAGE only works on Linux; use KCP_DIR to build from a checkout."
      command -v docker >/dev/null 2>&1 || die "docker is required to extract kcp from ${KCP_IMAGE}"
      log "extracting kcp from ${KCP_IMAGE}"
      docker pull --quiet "${KCP_IMAGE}" >/dev/null
      container="$(docker create "${KCP_IMAGE}")"
      mkdir -p "$(dirname "${KCP_BIN}")"
      docker cp "${container}:/kcp" "${KCP_BIN}" >/dev/null
      docker rm --force "${container}" >/dev/null
      chmod +x "${KCP_BIN}"
    else
      [[ -d "${KCP_DIR}" ]] || die "no kcp checkout at ${KCP_DIR}; set KCP_DIR=/path/to/kcp, or KCP_IMAGE on Linux"
      log "building kcp from ${KCP_DIR}"
      go build -C "${KCP_DIR}" -o "${KCP_BIN}" ./cmd/kcp
    fi
  fi
fi

[[ -x "${KCP_BIN}" ]] || die "no kcp binary at ${KCP_BIN}"
MARKETPLACE_BIN="${REPO_ROOT}/bin/marketplace-vw"
[[ -x "${MARKETPLACE_BIN}" ]] || die "no marketplace binary at ${MARKETPLACE_BIN}"

### PKI #######################################################################

# The virtual workspace authenticates callers by client certificate against
# this throwaway CA: CN becomes the username, O the groups. The PKI is
# reused between runs, so expiry is checked rather than only existence.
PKI_VALID_DAYS="${PKI_VALID_DAYS:-30}"
if [[ ! -f "${PKI_DIR}/alice.crt" ]] || ! openssl x509 -in "${PKI_DIR}/ca.crt" -checkend 3600 >/dev/null 2>&1; then
  log "generating a client CA and user certificates (valid ${PKI_VALID_DAYS} days)"
  rm -f "${PKI_DIR}"/*.crt "${PKI_DIR}"/*.key "${PKI_DIR}"/*.csr "${PKI_DIR}"/*.srl

  openssl req -x509 -newkey rsa:2048 -nodes -days "${PKI_VALID_DAYS}" \
    -keyout "${PKI_DIR}/ca.key" -out "${PKI_DIR}/ca.crt" \
    -subj "/CN=marketplace-e2e-ca" >>"${LOG_DIR}/pki.log" 2>&1

  for spec in "alice:team-a" "bob:team-b"; do
    user="${spec%%:*}"; group="${spec#*:}"
    openssl req -newkey rsa:2048 -nodes \
      -keyout "${PKI_DIR}/${user}.key" -out "${PKI_DIR}/${user}.csr" \
      -subj "/CN=${user}/O=${group}" >>"${LOG_DIR}/pki.log" 2>&1
    openssl x509 -req -in "${PKI_DIR}/${user}.csr" \
      -CA "${PKI_DIR}/ca.crt" -CAkey "${PKI_DIR}/ca.key" -CAcreateserial \
      -days "${PKI_VALID_DAYS}" -out "${PKI_DIR}/${user}.crt" >>"${LOG_DIR}/pki.log" 2>&1
  done
fi

### kcp #######################################################################

log "starting kcp on :${KCP_PORT}"
start_bg kcp "${KCP_BIN}" start \
  --root-directory="${KCP_ROOT_DIR}" \
  --secure-port="${KCP_PORT}" \
  --embedded-etcd-client-port="${ETCD_CLIENT_PORT}" \
  --embedded-etcd-peer-port="${ETCD_PEER_PORT}" \
  --client-ca-file="${PKI_DIR}/ca.crt" \
  --v="${KCP_V:-2}"

KUBECONFIG_PATH="${KCP_ROOT_DIR}/admin.kubeconfig"

log "waiting for kcp to be ready"
ready=false
for _ in $(seq 1 180); do
  require_alive kcp
  if [[ -f "${KUBECONFIG_PATH}" ]] && \
     kubectl --kubeconfig "${KUBECONFIG_PATH}" get --raw /readyz >/dev/null 2>&1; then
    ready=true
    break
  fi
  sleep 1
done
[[ "${ready}" == "true" ]] || die "kcp did not become ready, see ${LOG_DIR}/kcp.log"

### init (twice: the second run is the idempotency check) #####################

log "installing the marketplace APIExports (twice, for idempotency)"
"${MARKETPLACE_BIN}" init --kubeconfig "${KUBECONFIG_PATH}" >"${LOG_DIR}/init.log" 2>&1 \
  || { cat "${LOG_DIR}/init.log" >&2; die "init failed"; }
"${MARKETPLACE_BIN}" init --kubeconfig "${KUBECONFIG_PATH}" >>"${LOG_DIR}/init.log" 2>&1 \
  || { cat "${LOG_DIR}/init.log" >&2; die "second init failed; it must be idempotent"; }

### operator and virtual workspace ############################################

log "starting the marketplace operator"
start_bg operator "${MARKETPLACE_BIN}" operator \
  --kubeconfig "${KUBECONFIG_PATH}" \
  --workspace-path root:marketplace:controllers \
  --catalog-workspace root:marketplace:catalog \
  --producers-workspace root:marketplace:producers \
  --v=4

log "starting the marketplace virtual workspace on :${VW_PORT}"
start_bg vw "${MARKETPLACE_BIN}" virtualworkspace \
  --kubeconfig "${KUBECONFIG_PATH}" \
  --workspace-path root:marketplace:controllers \
  --secure-port "${VW_PORT}" \
  --client-ca-file "${PKI_DIR}/ca.crt" \
  --requestheader-client-ca-file "${PKI_DIR}/ca.crt" \
  `# Same CA on both flags, so the server insists on knowing which cert` \
  `# names may set identity headers. No minted cert carries this name.` \
  --requestheader-allowed-names marketplace-front-proxy \
  --v=4

log "waiting for the virtual workspace to serve"
vw_ready=false
for _ in $(seq 1 60); do
  require_alive vw
  if curl -ks "https://localhost:${VW_PORT}/readyz" | grep -q ok; then
    vw_ready=true
    break
  fi
  sleep 1
done
[[ "${vw_ready}" == "true" ]] || die "virtual workspace did not become ready, see ${LOG_DIR}/vw.log"

### tests #####################################################################

require_alive kcp
require_alive operator
require_alive vw

export KUBECONFIG="${KUBECONFIG_PATH}"
export MARKETPLACE_VW_URL="https://localhost:${VW_PORT}"
export MARKETPLACE_PKI_DIR="${PKI_DIR}"
export NO_TEARDOWN

WHAT="${WHAT:-./test/e2e/...}"
TEST_ARGS="${TEST_ARGS:--timeout 20m -v}"

log "running e2e tests: ${WHAT}"
# shellcheck disable=SC2086
go test -tags e2e -count=1 ${TEST_ARGS} ${WHAT}

log "e2e tests passed"
