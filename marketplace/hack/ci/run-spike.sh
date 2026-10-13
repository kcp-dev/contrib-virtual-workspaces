#!/usr/bin/env bash

# Spike #1 from ../DESIGN.md: prove the binder composition against a real
# kcp — an identity holding only the apibindings claim in a consumer
# workspace and a granted `bind` in one provider workspace can enable that
# provider's export there, and only that one.
#
# Nothing is built; there is no component binary yet. This starts kcp,
# mints the operator identity as a client certificate, and runs the test.
#
# Environment:
#   KCP_BIN    kcp binary (default: ../../kcp/bin/kcp relative to repo root)
#   KCP_PORT   default 7643; etcd on 7639/7640

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
KCP_BIN="${KCP_BIN:-${REPO_ROOT}/../kcp/bin/kcp}"
KCP_PORT="${KCP_PORT:-7643}"
ETCD_CLIENT_PORT="${ETCD_CLIENT_PORT:-7639}"
ETCD_PEER_PORT="${ETCD_PEER_PORT:-7640}"

WORK="${REPO_ROOT}/marketplace/.e2e-spike"
LOG_DIR="${WORK}/logs"
PKI_DIR="${WORK}/pki"
KCP_ROOT_DIR="${WORK}/.kcp"

log() { echo -e "\033[1;32m==>\033[0m $*"; }
die() { echo -e "\033[1;31mERROR:\033[0m $*" >&2; exit 1; }

[[ -x "${KCP_BIN}" ]] || die "no kcp binary at ${KCP_BIN}; set KCP_BIN or build ../kcp"

rm -rf "${WORK}"
mkdir -p "${LOG_DIR}" "${PKI_DIR}"

pids=()
stop_all() {
  for pid in "${pids[@]:-}"; do kill "${pid}" 2>/dev/null || true; done
  for _ in $(seq 1 20); do
    local alive=false
    for pid in "${pids[@]:-}"; do kill -0 "${pid}" 2>/dev/null && alive=true; done
    [[ "${alive}" == "false" ]] && return
    sleep 0.5
  done
  for pid in "${pids[@]:-}"; do kill -9 "${pid}" 2>/dev/null || true; done
}
trap stop_all EXIT

log "minting the operator identity (CN=mkt-operator)"
openssl req -x509 -newkey rsa:2048 -nodes -days 7 \
  -keyout "${PKI_DIR}/ca.key" -out "${PKI_DIR}/ca.crt" \
  -subj "/CN=marketplace-spike-ca" >"${LOG_DIR}/pki.log" 2>&1
openssl req -newkey rsa:2048 -nodes \
  -keyout "${PKI_DIR}/mkt-operator.key" -out "${PKI_DIR}/mkt-operator.csr" \
  -subj "/CN=mkt-operator" >>"${LOG_DIR}/pki.log" 2>&1
openssl x509 -req -in "${PKI_DIR}/mkt-operator.csr" \
  -CA "${PKI_DIR}/ca.crt" -CAkey "${PKI_DIR}/ca.key" -CAcreateserial \
  -days 7 -out "${PKI_DIR}/mkt-operator.crt" >>"${LOG_DIR}/pki.log" 2>&1

log "starting kcp on :${KCP_PORT}"
"${KCP_BIN}" start \
  --root-directory="${KCP_ROOT_DIR}" \
  --secure-port="${KCP_PORT}" \
  --embedded-etcd-client-port="${ETCD_CLIENT_PORT}" \
  --embedded-etcd-peer-port="${ETCD_PEER_PORT}" \
  --client-ca-file="${PKI_DIR}/ca.crt" \
  --v=2 >"${LOG_DIR}/kcp.log" 2>&1 &
pids+=($!)

KUBECONFIG_PATH="${KCP_ROOT_DIR}/admin.kubeconfig"
log "waiting for kcp to be ready"
ready=false
for _ in $(seq 1 180); do
  kill -0 "${pids[0]}" 2>/dev/null || die "kcp exited, see ${LOG_DIR}/kcp.log"
  if [[ -f "${KUBECONFIG_PATH}" ]] && \
     kubectl --kubeconfig "${KUBECONFIG_PATH}" get --raw /readyz >/dev/null 2>&1; then
    ready=true
    break
  fi
  sleep 1
done
[[ "${ready}" == "true" ]] || die "kcp did not become ready, see ${LOG_DIR}/kcp.log"

log "running the spike"
export KUBECONFIG="${KUBECONFIG_PATH}"
export MKT_PKI_DIR="${PKI_DIR}"
cd "${REPO_ROOT}"
go test -tags e2e -count=1 -timeout 10m -v ./marketplace/test/spike/... 2>&1 | tee "${LOG_DIR}/test.log"
result=${PIPESTATUS[0]}

if [[ "${result}" == "0" ]]; then
  log "spike PASSED"
else
  die "spike failed, logs in ${LOG_DIR}"
fi
