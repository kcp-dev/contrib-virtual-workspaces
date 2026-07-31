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

# Generates every certificate and kubeconfig needed to run the ephemeral
# virtual workspace against a kcp, and prints the two command lines that use
# them.
#
#   hack/gen-pki.sh --kcp-root ~/kcp/.kcp --out ./pki
#
# Point --kcp-root at kcp's --root-directory (its ".kcp"). Nothing in there is
# modified: the only thing read out of it is admin.kubeconfig, for kcp's serving
# CA and for a token to authenticate with.
#
# Run it before kcp has ever started and it emits the certificates and stops,
# because admin.kubeconfig does not exist yet; run it again afterwards and it
# adds the kubeconfig. Existing files are left alone, so re-running is safe.

set -euo pipefail

KCP_ROOT=".kcp"
OUT_DIR="pki"
KCP_SERVER=""
HOSTNAMES="localhost"
IPS="127.0.0.1"
VW_URL=""
SHARD_USER="kcp-shard"
SHARD_GROUP="system:kcp:shards"
KCP_USER="shard-admin"
EXAMPLE_CONFIG=""
WEBHOOK_URL=""
QUIET=false
CERTS_ONLY=false

log()  { [[ "${QUIET}" == true ]] || printf '\033[1;34m==>\033[0m %s\n' "$*" >&2; }
warn() { printf '\033[1;33m!!!\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[1;31mERR\033[0m %s\n' "$*" >&2; exit 1; }

usage() {
  sed -n '17,29p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//' >&2
  cat >&2 <<EOF

Options:
  --kcp-root DIR    kcp's --root-directory                 (default: ${KCP_ROOT})
  --out DIR         where to write certificates            (default: ${OUT_DIR})
  --server URL      kcp's base URL, no /clusters path      (default: from admin.kubeconfig)
  --vw-url URL      URL kcp will reach this server on      (default: https://<first hostname>:6454)
  --hostnames LIST  comma-separated DNS SANs               (default: ${HOSTNAMES})
  --ips LIST        comma-separated IP SANs                (default: ${IPS})
  --shard-user U    CN of the shard client certificate     (default: ${SHARD_USER})
  --shard-group G   O of the shard client certificate      (default: ${SHARD_GROUP})
  --kcp-user NAME   user in admin.kubeconfig to take a token from (default: ${KCP_USER})
  --example-config F  template for the starter ephemeral config
                      (default: docs/example/ephemeral-config.yaml)
  --webhook-url URL   webhook.url to write into the starter config
                      (default: the template's, examples/webhook on localhost)
  --certs-only      stop after the certificates, before the kubeconfig
  --quiet           only print warnings and errors, no summary
EOF
  exit "${1:-0}"
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --kcp-root)   KCP_ROOT=$2; shift 2 ;;
    --out)        OUT_DIR=$2; shift 2 ;;
    --server)     KCP_SERVER=$2; shift 2 ;;
    --vw-url)     VW_URL=$2; shift 2 ;;
    --hostnames)  HOSTNAMES=$2; shift 2 ;;
    --ips)        IPS=$2; shift 2 ;;
    --shard-user) SHARD_USER=$2; shift 2 ;;
    --shard-group) SHARD_GROUP=$2; shift 2 ;;
    --kcp-user)   KCP_USER=$2; shift 2 ;;
    --example-config) EXAMPLE_CONFIG=$2; shift 2 ;;
    --webhook-url) WEBHOOK_URL=$2; shift 2 ;;
    --quiet)      QUIET=true; shift ;;
    --certs-only) CERTS_ONLY=true; shift ;;
    -h|--help)    usage 0 ;;
    *)            warn "unknown argument $1"; usage 1 ;;
  esac
done

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
[[ -n "${EXAMPLE_CONFIG}" ]] || EXAMPLE_CONFIG="${SCRIPT_DIR}/../docs/example/ephemeral-config.yaml"

command -v openssl >/dev/null || die "openssl is required"
command -v kubectl >/dev/null || die "kubectl is required"

mkdir -p "${OUT_DIR}"
OUT_DIR=$(cd "${OUT_DIR}" && pwd)

FIRST_HOST=${HOSTNAMES%%,*}
[[ -n "${VW_URL}" ]] || VW_URL="https://${FIRST_HOST}:6454"

san=""
IFS=',' read -ra parts <<<"${HOSTNAMES}"
for h in "${parts[@]}"; do san+="DNS:${h},"; done
IFS=',' read -ra parts <<<"${IPS}"
for i in "${parts[@]}"; do san+="IP:${i},"; done
san=${san%,}

### certificates ##############################################################

# One CA. kcp never verifies any of these -- it only presents the shard client
# certificate -- so this CA is yours alone and no kcp CA configuration changes.
if [[ -f "${OUT_DIR}/ca.crt" ]]; then
  log "reusing CA ${OUT_DIR}/ca.crt"
else
  log "creating CA ${OUT_DIR}/ca.crt"
  openssl req -x509 -newkey rsa:2048 -nodes -days 365 \
    -keyout "${OUT_DIR}/ca.key" -out "${OUT_DIR}/ca.crt" \
    -subj "/CN=ephemeral-vw-ca" 2>/dev/null
fi

serving_cert() { # name, purpose
  local name=$1 purpose=$2
  if [[ -f "${OUT_DIR}/${name}.crt" ]]; then
    log "reusing ${OUT_DIR}/${name}.crt"
    return
  fi
  log "creating ${OUT_DIR}/${name}.crt (${purpose}), SANs ${san}"
  openssl req -newkey rsa:2048 -nodes \
    -keyout "${OUT_DIR}/${name}.key" -out "${OUT_DIR}/${name}.csr" \
    -subj "/CN=${FIRST_HOST}" 2>/dev/null
  openssl x509 -req -in "${OUT_DIR}/${name}.csr" -days 365 \
    -CA "${OUT_DIR}/ca.crt" -CAkey "${OUT_DIR}/ca.key" -CAcreateserial \
    -out "${OUT_DIR}/${name}.crt" \
    -extfile <(printf 'subjectAltName=%s\nextendedKeyUsage=serverAuth\n' "${san}") 2>/dev/null
  rm -f "${OUT_DIR}/${name}.csr"
}

client_cert() { # name, cn, o, purpose
  local name=$1 cn=$2 org=$3 purpose=$4
  if [[ -f "${OUT_DIR}/${name}.crt" ]]; then
    log "reusing ${OUT_DIR}/${name}.crt"
    return
  fi
  log "creating ${OUT_DIR}/${name}.crt (${purpose}), CN=${cn} O=${org}"
  openssl req -newkey rsa:2048 -nodes \
    -keyout "${OUT_DIR}/${name}.key" -out "${OUT_DIR}/${name}.csr" \
    -subj "/O=${org}/CN=${cn}" 2>/dev/null
  openssl x509 -req -in "${OUT_DIR}/${name}.csr" -days 365 \
    -CA "${OUT_DIR}/ca.crt" -CAkey "${OUT_DIR}/ca.key" -CAcreateserial \
    -out "${OUT_DIR}/${name}.crt" \
    -extfile <(printf 'extendedKeyUsage=clientAuth\nkeyUsage=digitalSignature\n') 2>/dev/null
  rm -f "${OUT_DIR}/${name}.csr"
}

serving_cert vw "this server's serving certificate"
serving_cert webhook "the example webhook's serving certificate"

# Deliberately not system:masters: the virtual workspace framework's
# AlwaysAllowGroups short-circuits on it and this server's own authorizer --
# create-only, binding required -- would never run.
client_cert shard-client "${SHARD_USER}" "${SHARD_GROUP}" "the identity kcp presents when it proxies"

### starter config ############################################################

# A copy of docs/example/ephemeral-config.yaml with the paths this script knows
# filled in. Its webhook URL points at examples/webhook on localhost, which is
# runnable as it stands; --webhook-url replaces it with a real endpoint.
CONFIG_FILE="${OUT_DIR}/ephemeral-config.yaml"
if [[ -f "${CONFIG_FILE}" ]]; then
  log "reusing ${CONFIG_FILE}"
elif [[ ! -f "${EXAMPLE_CONFIG}" ]]; then
  warn "no template at ${EXAMPLE_CONFIG}, not writing a starter config"
  CONFIG_FILE=""
else
  # caBundleFile points at our CA, which signed webhook.crt. The client
  # certificate lines are commented out rather than pointed somewhere: mTLS to
  # the webhook is a decision between you and the provider, and a path to a
  # file that does not exist stops the server from starting.
  sed -e "s|^\( *caBundleFile:\).*|\1 ${OUT_DIR}/ca.crt|" \
      -e "s|^\( *clientCertFile:.*\)$|#\1|" \
      -e "s|^\( *clientKeyFile:.*\)$|#\1|" \
      "${EXAMPLE_CONFIG}" >"${CONFIG_FILE}"
  if [[ -n "${WEBHOOK_URL}" ]]; then
    sed -i.bak -e "s|^\( *url:\).*|\1 ${WEBHOOK_URL}|" "${CONFIG_FILE}" && rm -f "${CONFIG_FILE}.bak"
    log "wrote ${CONFIG_FILE} from ${EXAMPLE_CONFIG}, webhook.url=${WEBHOOK_URL}"
  else
    log "wrote ${CONFIG_FILE} from ${EXAMPLE_CONFIG}"
  fi
fi

### kubeconfig ################################################################

ADMIN_KUBECONFIG="${KCP_ROOT}/admin.kubeconfig"

if [[ "${CERTS_ONLY}" == true ]]; then
  KUBECONFIG_READY=false
elif [[ ! -f "${ADMIN_KUBECONFIG}" ]]; then
  warn "no ${ADMIN_KUBECONFIG} yet, so ${OUT_DIR}/vw-client.kubeconfig was not written."
  warn "Start kcp once with the flags below, then run this script again."
  KUBECONFIG_READY=false
else
  KUBECONFIG_READY=true

  kubectl --kubeconfig="${ADMIN_KUBECONFIG}" config view --raw \
    -o jsonpath='{.clusters[0].cluster.certificate-authority-data}' \
    | base64 -d >"${OUT_DIR}/kcp-ca.crt"
  [[ -s "${OUT_DIR}/kcp-ca.crt" ]] || die "no CA data in ${ADMIN_KUBECONFIG}"
  log "extracted kcp's serving CA to ${OUT_DIR}/kcp-ca.crt"

  # The identity has to be privileged: this server's informers watch every
  # logical cluster at once, and kcp refuses wildcard list/watch to ordinary
  # users. In a plain `kcp start` only shard-admin qualifies; kcp-admin does not.
  token=$(kubectl --kubeconfig="${ADMIN_KUBECONFIG}" config view --raw \
    -o jsonpath="{.users[?(@.name==\"${KCP_USER}\")].user.token}")
  [[ -n "${token}" ]] || die "no token for user ${KCP_USER} in ${ADMIN_KUBECONFIG} (try --kcp-user)"

  if [[ -z "${KCP_SERVER}" ]]; then
    KCP_SERVER=$(kubectl --kubeconfig="${ADMIN_KUBECONFIG}" config view --raw \
      -o jsonpath='{.clusters[?(@.name=="base")].cluster.server}')
    [[ -n "${KCP_SERVER}" ]] || KCP_SERVER=$(kubectl --kubeconfig="${ADMIN_KUBECONFIG}" \
      config view --raw -o jsonpath='{.clusters[0].cluster.server}')
  fi
  # The clients add /clusters/<name> themselves, so any path has to go.
  KCP_SERVER=${KCP_SERVER%%/clusters/*}

  log "writing ${OUT_DIR}/vw-client.kubeconfig (user ${KCP_USER}, server ${KCP_SERVER})"
  cat >"${OUT_DIR}/vw-client.kubeconfig" <<EOF
apiVersion: v1
kind: Config
clusters:
- name: shard
  cluster:
    server: ${KCP_SERVER}
    certificate-authority: ${OUT_DIR}/kcp-ca.crt
users:
- name: vw
  user:
    token: ${token}
contexts:
- name: shard
  context:
    cluster: shard
    user: vw
current-context: shard
EOF
  chmod 600 "${OUT_DIR}/vw-client.kubeconfig"
fi

### what to do with them ######################################################

if [[ "${QUIET}" == true ]]; then
  exit 0
fi

# The server refuses loopback and private webhook addresses unless told
# otherwise, so if the config it will read names one, say so here rather than
# leaving it to be discovered as
# "webhook address 127.0.0.1 is a loopback address" at the first request.
PRIVATE_WEBHOOK_FLAG=""
if [[ -n "${CONFIG_FILE}" && -f "${CONFIG_FILE}" ]]; then
  configured_url=$(grep -m1 -E '^[[:space:]]*url:' "${CONFIG_FILE}" | awk '{print $2}')
  configured_host=${configured_url#*://}
  configured_host=${configured_host%%[:/]*}
  case "${configured_host}" in
    localhost|127.*|::1|\[::1\]|0.0.0.0|10.*|192.168.*|172.1[6-9].*|172.2[0-9].*|172.3[01].*)
      PRIVATE_WEBHOOK_FLAG=$'\n    --allow-private-webhook-addresses \\'
      ;;
  esac
fi

cat >&2 <<EOF

--------------------------------------------------------------------------------
Start kcp with (in addition to whatever it already runs with):

  --feature-gates=CacheAPIs=true \\
  --shard-virtual-workspace-url=${VW_URL} \\
  --shard-virtual-workspace-ca-file=${OUT_DIR}/ca.crt \\
  --shard-client-cert-file=${OUT_DIR}/shard-client.crt \\
  --shard-client-key-file=${OUT_DIR}/shard-client.key
EOF

if [[ "${KUBECONFIG_READY}" == true ]]; then
  cat >&2 <<EOF

Then start this server with:

  ./bin/ephemeral-virtual-workspace \\
    --ephemeral-config=${CONFIG_FILE:-<your ephemeral-config.yaml>} \\
    --secure-port=6454 \\
    --tls-cert-file=${OUT_DIR}/vw.crt \\
    --tls-private-key-file=${OUT_DIR}/vw.key \\
    --client-ca-file=${OUT_DIR}/ca.crt \\
    --requestheader-client-ca-file=${OUT_DIR}/ca.crt \\
    --requestheader-allowed-names=${SHARD_USER} \\
    --requestheader-username-headers=X-Remote-User \\
    --requestheader-group-headers=X-Remote-Group \\
    --requestheader-extra-headers-prefix=X-Remote-Extra- \\
    --kubeconfig=${OUT_DIR}/vw-client.kubeconfig \\
    --cache-kubeconfig=${OUT_DIR}/vw-client.kubeconfig \\
    --authentication-kubeconfig=${OUT_DIR}/vw-client.kubeconfig \\${PRIVATE_WEBHOOK_FLAG}
    --authentication-skip-lookup

Before that works, check ${CONFIG_FILE:-your config}:
  * export.path/name, group and resource must match your APIExport;
  * webhook.url points at examples/webhook on localhost. That is why
    --allow-private-webhook-addresses is in the command above: loopback and
    private addresses are refused by default. Point it at a real endpoint and
    drop the flag;
  * clientCertFile/clientKeyFile are commented out, so this server presents no
    client certificate to the webhook. Enable them if the provider verifies one.

The --requestheader-* flags make this server believe the caller identity a shard
forwards, over a connection ca.crt signed and from ${SHARD_USER} only. A kcp
that does not forward identity is unaffected by them: the request then arrives
as ${SHARD_USER} and needs RBAC granted to that user instead.

${OUT_DIR}/webhook.crt and .key are for the example webhook; a real provider
brings its own, and caBundleFile then points at whatever signed it.
--------------------------------------------------------------------------------
EOF
else
  printf -- '--------------------------------------------------------------------------------\n' >&2
fi
