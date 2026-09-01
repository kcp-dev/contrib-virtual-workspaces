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

# Brings up a single-shard kcp, the example webhook and this virtual workspace,
# applies the example manifests and exercises the request path end to end.
#
# The routing problem docs/routing.md describes is sidestepped rather than
# solved: --shard-virtual-workspace-url points straight at this server, so no
# path router is needed and no other virtual workspace is reachable through the
# URL kcp advertises. That is fine for a demo and wrong for a deployment.
#
# kcp keeps its embedded virtual workspaces running even though nothing reaches
# them through the advertised URL. Its own APIBindings initializer talks to the
# initializingworkspaces virtual workspace, and only falls back to
# --shard-virtual-workspace-url when embedded ones are switched off -- turn them
# off here and no workspace ever leaves Initializing.
#
#   hack/local-up.sh          bring everything up and run the checks
#   hack/local-up.sh down     stop everything
#   hack/local-up.sh clean    stop everything and delete the work directory

set -euo pipefail

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORK_DIR="${WORK_DIR:-${REPO_DIR}/.local}"
KCP_DIR="${KCP_DIR:-$(cd "${REPO_DIR}/.." && pwd)/kcp}"
# Prefer a kcp binary in this repo's bin/ -- `make kcp` puts one there -- so
# that everything runs from this directory, where the PKI and work dir live.
# kcp is a separate Go module and cannot be built or run from this one.
if [[ -z "${KCP_BIN:-}" ]]; then
  if [[ -x "${REPO_DIR}/bin/kcp" ]]; then
    KCP_BIN="${REPO_DIR}/bin/kcp"
  else
    KCP_BIN="${KCP_DIR}/bin/kcp"
  fi
fi

PKI_DIR="${WORK_DIR}/pki"
LOG_DIR="${WORK_DIR}/logs"
RUN_DIR="${WORK_DIR}/run"

# kcp's --root-directory: the ".kcp" a plain `kcp start` writes.
KCP_ROOT_DIR="${KCP_ROOT_DIR:-${WORK_DIR}/kcp}"

KCP_PORT="${KCP_PORT:-6443}"
VW_PORT="${VW_PORT:-6454}"
# kcp's embedded etcd binds these regardless of --secure-port, so moving
# KCP_PORT alone is not enough to run alongside another kcp on the same machine.
ETCD_CLIENT_PORT="${ETCD_CLIENT_PORT:-2379}"
ETCD_PEER_PORT="${ETCD_PEER_PORT:-2380}"
# Not 8443: that one is routinely taken by a kubectl port-forward, and the
# failure mode is a confusing "tls: bad certificate" from whatever answers
# instead of the webhook.
WEBHOOK_PORT="${WEBHOOK_PORT:-18443}"

PROVIDER_WS="root:providers:s3"
CONSUMER_WS="root:consumer"
EXPORT_NAME="s3.example.com"
# The path segment this server answers on. Named after the service it provides
# rather than "apiexport", which it no longer has to squat on.
VW_NAME="${VW_NAME:-ephemeral-buckets}"

# The identity kcp presents to this virtual workspace when it proxies a
# consumer request. Deliberately not system:masters: the framework's
# AlwaysAllowGroups would short-circuit authorization and the virtual
# workspace's own authorizer would never run.
SHARD_USER="kcp-shard"
SHARD_GROUP="system:kcp:demo-shards"

log()  { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m!!!\033[0m %s\n' "$*"; }
die()  { printf '\033[1;31mERR\033[0m %s\n' "$*" >&2; exit 1; }

### process handling ##########################################################

start_bg() { # name, then command
  local name=$1; shift
  mkdir -p "${LOG_DIR}" "${RUN_DIR}"
  "$@" >"${LOG_DIR}/${name}.log" 2>&1 &
  echo $! >"${RUN_DIR}/${name}.pid"
  log "started ${name} (pid $(cat "${RUN_DIR}/${name}.pid")), logging to ${LOG_DIR}/${name}.log"
}

stop_all() {
  [[ -d "${RUN_DIR}" ]] || return 0
  for pidfile in "${RUN_DIR}"/*.pid; do
    [[ -e "${pidfile}" ]] || continue
    local pid name
    pid=$(cat "${pidfile}")
    name=$(basename "${pidfile}" .pid)
    if kill -0 "${pid}" 2>/dev/null; then
      log "stopping ${name} (pid ${pid})"
      kill "${pid}" 2>/dev/null || true
      for _ in $(seq 1 50); do
        kill -0 "${pid}" 2>/dev/null || break
        sleep 0.1
      done
      kill -9 "${pid}" 2>/dev/null || true
    fi
    rm -f "${pidfile}"
  done
}

alive() { # name
  local pidfile="${RUN_DIR}/$1.pid"
  [[ -f "${pidfile}" ]] && kill -0 "$(cat "${pidfile}")" 2>/dev/null
}

require_alive() { # name
  alive "$1" || die "$1 died on startup, see ${LOG_DIR}/$1.log"
}

### pki #######################################################################

# Everything here is done by hack/gen-pki.sh, which is the same script the
# documentation tells people to run against their own kcp. It is called twice:
# once before kcp starts, when it can only write certificates because
# admin.kubeconfig does not exist yet, and once after, for the kubeconfig.
gen_pki() { # extra args
  "${REPO_DIR}/hack/gen-pki.sh" \
    --kcp-root "${KCP_ROOT_DIR}" \
    --out "${PKI_DIR}" \
    --server "https://localhost:${KCP_PORT}" \
    --vw-url "https://localhost:${VW_PORT}" \
    --shard-user "${SHARD_USER}" \
    --shard-group "${SHARD_GROUP}" \
    --webhook-url "https://localhost:${WEBHOOK_PORT}/ephemeral/bucketinfos" \
    --quiet "$@"
}

### kcp #######################################################################

start_kcp() {
  [[ -x "${KCP_BIN}" ]] || die "no kcp binary at ${KCP_BIN}; run 'make kcp' (or set KCP_BIN=/path/to/kcp)"

  log "starting kcp on :${KCP_PORT}"
  start_bg kcp "${KCP_BIN}" start \
    --root-directory="${KCP_ROOT_DIR}" \
    --secure-port="${KCP_PORT}" \
    --embedded-etcd-client-port="${ETCD_CLIENT_PORT}" \
    --embedded-etcd-peer-port="${ETCD_PEER_PORT}" \
    --feature-gates=CacheAPIs=true \
    --shard-virtual-workspace-url="https://localhost:${VW_PORT}" \
    --shard-virtual-workspace-ca-file="${PKI_DIR}/ca.crt" \
    --shard-client-cert-file="${PKI_DIR}/shard-client.crt" \
    --shard-client-key-file="${PKI_DIR}/shard-client.key" \
    --v="${KCP_V:-2}"

  local kubeconfig="${KCP_ROOT_DIR}/admin.kubeconfig"
  log "waiting for kcp to be ready"
  for _ in $(seq 1 180); do
    require_alive kcp
    if [[ -f "${kubeconfig}" ]] && \
       kubectl --kubeconfig="${kubeconfig}" get --raw /readyz >/dev/null 2>&1; then
      log "kcp is ready"
      return
    fi
    sleep 1
  done
  die "kcp did not become ready, see ${LOG_DIR}/kcp.log"
}

# Extracts server, CA and token once so that every later call can address a
# workspace by path without mutating a kubeconfig.
load_client_config() {
  local kubeconfig="${KCP_ROOT_DIR}/admin.kubeconfig"

  # Now that admin.kubeconfig exists, the second pass writes the kubeconfig this
  # server authenticates to kcp with, and extracts kcp's serving CA.
  log "writing ${PKI_DIR}/vw-client.kubeconfig"
  gen_pki
  [[ -f "${PKI_DIR}/vw-client.kubeconfig" ]] || die "hack/gen-pki.sh did not write vw-client.kubeconfig"

  KCP_SERVER="https://localhost:${KCP_PORT}"
  KCP_CA="${PKI_DIR}/kcp-ca.crt"
  KCP_TOKEN=$(kubectl --kubeconfig="${kubeconfig}" config view --raw \
    -o jsonpath='{.users[0].user.token}')
  [[ -n "${KCP_TOKEN}" ]] || die "could not read an admin token out of ${kubeconfig}"
}

kc() { # workspace-path, then kubectl args
  local ws=$1; shift
  # Discovery is never cached here, because this script's whole job is to apply
  # things and use them immediately: a CRD, then an object of that kind; an
  # APIExport, then a binding to it. kubectl keeps what it saw under
  # ~/.kube/cache for hours, so a cached answer is one taken before the thing
  # existed -- "no matches for kind" for something applied a second earlier.
  #
  # A private directory rather than ~/.kube/cache, so that clearing it cannot
  # disturb the user's own kubectl.
  rm -rf "${RUN_DIR}/kubectl-cache"
  kubectl --server="${KCP_SERVER}/clusters/${ws}" \
          --certificate-authority="${KCP_CA}" \
          --token="${KCP_TOKEN}" \
          --cache-dir="${RUN_DIR}/kubectl-cache" "$@"
}

workspace() { # parent-path, name
  local parent=$1 name=$2
  if kc "${parent}" get workspace "${name}" >/dev/null 2>&1; then
    return
  fi
  log "creating workspace ${parent}:${name}"
  kc "${parent}" create -f - >/dev/null <<EOF
apiVersion: tenancy.kcp.io/v1alpha1
kind: Workspace
metadata:
  name: ${name}
EOF
  for _ in $(seq 1 60); do
    [[ "$(kc "${parent}" get workspace "${name}" -o jsonpath='{.status.phase}')" == "Ready" ]] && return
    sleep 1
  done
  die "workspace ${parent}:${name} did not become ready"
}

### webhook ###################################################################

start_webhook() {
  [[ -x "${REPO_DIR}/bin/example-webhook" ]] || die "run 'make build' first"
  log "starting example webhook on :${WEBHOOK_PORT}"
  start_bg webhook "${REPO_DIR}/bin/example-webhook" \
    --address=":${WEBHOOK_PORT}" \
    --tls-cert-file="${PKI_DIR}/webhook.crt" \
    --tls-private-key-file="${PKI_DIR}/webhook.key"

  for _ in $(seq 1 30); do
    require_alive webhook
    if curl -sf --cacert "${PKI_DIR}/ca.crt" "https://localhost:${WEBHOOK_PORT}/healthz" >/dev/null; then
      log "webhook is ready"
      return
    fi
    sleep 1
  done
  die "webhook did not become ready, see ${LOG_DIR}/webhook.log"
}

### provider side #############################################################

apply_provider_manifests() {
  workspace root providers
  workspace root:providers s3
  workspace root consumer

  log "applying the EphemeralResourceEndpointSlice CRD"
  kc "${PROVIDER_WS}" apply -f "${REPO_DIR}/docs/example/00-endpointslice-crd.yaml" >/dev/null

  # Applying a CRD and creating one of its objects are not the same instant:
  # the kind has to be established and served first. Skipping this wait fails
  # later as `no matches for kind "EphemeralResourceEndpointSlice"`, which reads
  # like the CRD was never applied.
  local crd_served=false
  for _ in $(seq 1 60); do
    if kc "${PROVIDER_WS}" api-resources --api-group=ephemeral.contrib.kcp.io 2>/dev/null \
       | grep -q '^ephemeralresourceendpointslices[[:space:]]'; then
      crd_served=true
      break
    fi
    sleep 1
  done
  [[ "${crd_served}" == true ]] || die "the EphemeralResourceEndpointSlice CRD never became servable in ${PROVIDER_WS}"

  log "applying the APIResourceSchema"
  kc "${PROVIDER_WS}" apply -f "${REPO_DIR}/docs/example/01-apiresourceschema.yaml" >/dev/null

  # The APIExport has to exist before its identity hash does, so it is created
  # with the placeholder and patched afterwards.
  log "applying the APIExport"
  sed 's/REPLACE_WITH_APIEXPORT_STATUS_IDENTITYHASH/pending/' \
    "${REPO_DIR}/docs/example/02-apiexport.yaml" | kc "${PROVIDER_WS}" apply -f - >/dev/null

  local identity=""
  for _ in $(seq 1 60); do
    identity=$(kc "${PROVIDER_WS}" get apiexport "${EXPORT_NAME}" -o jsonpath='{.status.identityHash}')
    [[ -n "${identity}" ]] && break
    sleep 1
  done
  [[ -n "${identity}" ]] || die "APIExport ${EXPORT_NAME} never got an identity hash"

  # A JSON patch addresses spec.resources by position, and this export mixes
  # storage kinds -- buckets is crd, bucketinfos is virtual. Assuming an index
  # fails as "the request is invalid", because the path does not exist rather
  # than because the value is wrong, so it is looked up instead.
  local index
  index=$(kc "${PROVIDER_WS}" get apiexport "${EXPORT_NAME}" \
            -o jsonpath='{range .spec.resources[*]}{.name}{"\n"}{end}' \
          | grep -n -x "bucketinfos" | cut -d: -f1) || true
  [[ -n "${index}" ]] || die "APIExport ${EXPORT_NAME} has no bucketinfos resource to patch"
  index=$((index - 1))

  log "setting resources[${index}] (bucketinfos) storage.virtual.identityHash to ${identity}"
  kc "${PROVIDER_WS}" patch apiexport "${EXPORT_NAME}" --type=json \
    -p "[{\"op\":\"replace\",\"path\":\"/spec/resources/${index}/storage/virtual/identityHash\",\"value\":\"${identity}\"}]" >/dev/null

  # The object the APIExport references. Its status is empty until this server
  # starts and publishes its own address into it -- no kcp controller fills this
  # one in, which is the point.
  log "applying the EphemeralResourceEndpointSlice"
  kc "${PROVIDER_WS}" apply -f "${REPO_DIR}/docs/example/03-endpointslice.yaml" >/dev/null

  # The proxied request arrives as this shard identity, not as the end user, so
  # this is what --require-export-content-authorization ends up checking.
  log "granting ${SHARD_USER} create on apiexports/content"
  kc "${PROVIDER_WS}" apply -f - >/dev/null <<EOF
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: ephemeral-demo-export-content
rules:
# kcp's workspace content authorizer gates everything else behind verb=access
# on "/", so a grant without this one is never even consulted.
- nonResourceURLs: ["/"]
  verbs: ["access"]
- apiGroups: ["apis.kcp.io"]
  resources: ["apiexports/content"]
  resourceNames: ["${EXPORT_NAME}"]
  verbs: ["create"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: ephemeral-demo-export-content
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: ephemeral-demo-export-content
subjects:
- apiGroup: rbac.authorization.k8s.io
  kind: User
  name: ${SHARD_USER}
- apiGroup: rbac.authorization.k8s.io
  kind: Group
  name: ${SHARD_GROUP}
EOF
}

### virtual workspace #########################################################


start_vw() {
  [[ -x "${REPO_DIR}/bin/ephemeral-virtual-workspace" ]] || die "run 'make build' first"

  # The config is the one hack/gen-pki.sh generated from
  # docs/example/ephemeral-config.yaml -- the same file the documentation tells
  # people to use -- rather than a copy written here. Only the webhook URL is
  # substituted, so the export it names has to be the one this script applies.
  local config="${PKI_DIR}/ephemeral-config.yaml"
  [[ -f "${config}" ]] || die "hack/gen-pki.sh did not write ${config}"
  grep -q "path: ${PROVIDER_WS}$" "${config}" && grep -q "name: ${EXPORT_NAME}$" "${config}" \
    || die "${config} does not name the export this script applies (${PROVIDER_WS}|${EXPORT_NAME}); docs/example/ephemeral-config.yaml has drifted"

  log "starting the ephemeral virtual workspace on :${VW_PORT}"
  # Same flags, same order as the README and hack/gen-pki.sh print, so the two
  # can be compared line by line. --v=4 is the only addition.
  start_bg vw "${REPO_DIR}/bin/ephemeral-virtual-workspace" \
    --ephemeral-config="${config}" \
    --secure-port="${VW_PORT}" \
    --tls-cert-file="${PKI_DIR}/vw.crt" \
    --tls-private-key-file="${PKI_DIR}/vw.key" \
    --client-ca-file="${PKI_DIR}/ca.crt" \
    `# Believe the identity the shard forwards, but only over a connection` \
    `# whose client certificate this CA signed, and only from the shard.` \
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

  for _ in $(seq 1 60); do
    require_alive vw
    if curl -sf --cacert "${PKI_DIR}/ca.crt" "https://localhost:${VW_PORT}/readyz" >/dev/null; then
      log "virtual workspace is ready"
      return
    fi
    sleep 1
  done
  die "the virtual workspace did not become ready, see ${LOG_DIR}/vw.log"
}

### endpoint slice controller #################################################

# A separate process, running against the provider's workspace: it writes where
# the virtual workspace is into the slice the APIExport references. The virtual
# workspace does not do this itself -- it serves every workspace that binds the
# export, while this only ever touches one provider's objects.
start_endpointslice_controller() {
  [[ -x "${REPO_DIR}/bin/endpointslice-controller" ]] || die "run 'make build' first"

  # A workspace-scoped kubeconfig: the server URL carries the workspace, so the
  # controller needs no wildcard access and cannot touch anything else.
  local kubeconfig="${PKI_DIR}/provider.kubeconfig"
  cat >"${kubeconfig}" <<EOF
apiVersion: v1
kind: Config
clusters:
- name: provider
  cluster:
    server: ${KCP_SERVER}/clusters/${PROVIDER_WS}
    certificate-authority: ${KCP_CA}
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
  chmod 600 "${kubeconfig}"

  log "starting the endpoint slice controller against ${PROVIDER_WS}"
  start_bg endpointslice "${REPO_DIR}/bin/endpointslice-controller" \
    --kubeconfig="${kubeconfig}" \
    --external-url="https://localhost:${VW_PORT}" \
    --virtual-workspace-name="${VW_NAME}" \
    --export="${EXPORT_NAME}" \
    --v=2
}

### consumer side #############################################################

bind_and_verify() {
  log "applying the APIBinding in ${CONSUMER_WS}"
  kc "${CONSUMER_WS}" apply -f "${REPO_DIR}/docs/example/04-apibinding.yaml" >/dev/null

  # Kept for a shard that does not forward the caller's identity.
  #
  # With identity forwarding the virtual workspace authorizes the person who ran
  # kubectl, and these grants to the shard are unnecessary -- that is the whole
  # point of the change. They are harmless when unused, and are what makes this
  # script still work against a kcp without it.
  log "granting ${SHARD_USER} create on bucketinfos in the consumer workspace"
  kc "${CONSUMER_WS}" apply -f - >/dev/null <<EOF
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: ephemeral-demo-bucketinfos
rules:
- nonResourceURLs: ["/"]
  verbs: ["access"]
- apiGroups: ["s3.example.com"]
  resources: ["bucketinfos"]
  verbs: ["create"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: ephemeral-demo-bucketinfos
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: ephemeral-demo-bucketinfos
subjects:
- apiGroup: rbac.authorization.k8s.io
  kind: User
  name: ${SHARD_USER}
- apiGroup: rbac.authorization.k8s.io
  kind: Group
  name: ${SHARD_GROUP}
EOF

  for _ in $(seq 1 60); do
    if [[ "$(kc "${CONSUMER_WS}" get apibinding "${EXPORT_NAME}" \
              -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}')" == "True" ]]; then
      break
    fi
    sleep 1
  done
  kc "${CONSUMER_WS}" get apibinding "${EXPORT_NAME}" \
    -o jsonpath='{.status.phase}{"\n"}' | grep -q Bound || die "the APIBinding never bound"
  log "APIBinding is bound"

  local url=""
  for _ in $(seq 1 60); do
    url=$(kc "${PROVIDER_WS}" get ephemeralresourceendpointslice "${EXPORT_NAME}" -o jsonpath='{.status.endpoints[0].url}' 2>/dev/null || true)
    [[ -n "${url}" ]] && break
    sleep 1
  done
  [[ -n "${url}" ]] || die "this server never published its address into the EphemeralResourceEndpointSlice"
  log "published endpoint: ${url}"

  # A bound APIBinding does not mean bucketinfos can be served yet.
  #
  # The shard can only serve it once it can read the endpoint slice out of the
  # cache server, and the reference replication controller gets it there on a
  # poll rather than a watch. Until it does, discovery answers "temporarily
  # unavailable" and a create fails with `no matches for kind "BucketInfo"` --
  # which looks like a broken setup and is really just being early.
  log "waiting for bucketinfos to become servable"
  local served=false
  for _ in $(seq 1 60); do
    # Ask through discovery, which is what a client uses. kc discards the
    # discovery cache on every call, so this really does re-ask each time.
    #
    # Not "get --raw /apis/s3.example.com/v1alpha1". That path is a
    # non-resource request, which kcp gates behind verb=access on "/", so it is
    # forbidden for an ordinary admin -- and kubectl never asks for it anyway,
    # because aggregated discovery answers from /apis in one round trip.
    if kc "${CONSUMER_WS}" api-resources --api-group=s3.example.com 2>/dev/null \
       | grep -q '^bucketinfos[[:space:]]'; then
      served=true
      break
    fi
    sleep 1
  done
  [[ "${served}" == true ]] || die "bucketinfos never became servable; look for \"failed to retrieve virtual workspace URL\" in ${LOG_DIR}/kcp.log"

  echo
  log "discovery: buckets get the usual verbs, bucketinfos only create"
  kc "${CONSUMER_WS}" api-resources --api-group=s3.example.com -o wide

  echo
  log "the CRD-backed half: a Bucket is a record, stored and listable"
  kc "${CONSUMER_WS}" create -f "${REPO_DIR}/docs/example/bucket.yaml"
  kc "${CONSUMER_WS}" get buckets

  echo
  log "create: the webhook answers and the answer comes back, unstored"
  kc "${CONSUMER_WS}" create -f "${REPO_DIR}/docs/example/bucketinfo.yaml" -o yaml

  echo
  log "get: refused for bucketinfos, though buckets in the same export list fine"
  kc "${CONSUMER_WS}" get bucketinfos 2>&1 | tail -1 || true

  echo
  log "a denial from the webhook becomes a real API error"
  kc "${CONSUMER_WS}" create -f - 2>&1 <<'EOF' | tail -1 || true
apiVersion: s3.example.com/v1alpha1
kind: BucketInfo
spec:
  bucketName: does-not-exist
EOF

  echo
  log "dry-run reaches the webhook as dryRun: true (see ${LOG_DIR}/webhook.log)"
  kc "${CONSUMER_WS}" create -f "${REPO_DIR}/docs/example/bucketinfo.yaml" \
    --dry-run=server -o jsonpath='{.status}{"\n"}'
}

### main ######################################################################

case "${1:-up}" in
  down)
    stop_all
    exit 0
    ;;
  clean)
    stop_all
    log "removing ${WORK_DIR}"
    rm -rf "${WORK_DIR}"
    exit 0
    ;;
  up) ;;
  *) die "usage: $0 [up|down|clean]" ;;
esac

mkdir -p "${WORK_DIR}" "${LOG_DIR}" "${RUN_DIR}"
stop_all

# Bind on the loopback addresses a client reaches through "localhost", not just
# the wildcard: a process holding 127.0.0.1:<port> wins over a wildcard listener
# and answers in its place, which shows up much later as a TLS error.
for port_and_name in "${KCP_PORT}:kcp" "${VW_PORT}:virtual workspace" "${WEBHOOK_PORT}:webhook" \
                     "${ETCD_CLIENT_PORT}:embedded etcd client" "${ETCD_PEER_PORT}:embedded etcd peer"; do
  port=${port_and_name%%:*}
  if lsof -nP -iTCP:"${port}" -sTCP:LISTEN >/dev/null 2>&1; then
    lsof -nP -iTCP:"${port}" -sTCP:LISTEN 2>/dev/null | tail -n +2 >&2
    die "port ${port} (${port_and_name#*:}) is already in use; free it or set $(
      case ${port} in
        "${KCP_PORT}") echo KCP_PORT ;;
        "${VW_PORT}") echo VW_PORT ;;
        "${WEBHOOK_PORT}") echo WEBHOOK_PORT ;;
        "${ETCD_CLIENT_PORT}") echo ETCD_CLIENT_PORT ;;
        *) echo ETCD_PEER_PORT ;;
      esac)"
  fi
done

gen_pki --certs-only   # no admin.kubeconfig before kcp's first start
start_kcp
load_client_config
start_webhook
apply_provider_manifests
start_vw
start_endpointslice_controller
bind_and_verify

echo
log "everything is up. logs are in ${LOG_DIR}"
log "talk to the consumer workspace with:"
cat <<EOF

  kubectl --server=${KCP_SERVER}/clusters/${CONSUMER_WS} \\
          --certificate-authority=${KCP_CA} \\
          --token=<see ${KCP_ROOT_DIR}/admin.kubeconfig> \\
          create -f docs/example/bucketinfo.yaml -o yaml

  $0 down    # stop
  $0 clean   # stop and wipe ${WORK_DIR}
EOF
