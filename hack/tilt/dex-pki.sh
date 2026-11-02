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

# Mints the Dex serving certificate for the Tilt setup: a single
# self-signed cert for dex.kcp.localhost, minted on the host so the same
# CA file both goes into the cluster (secret dex-tls, which kcp's
# spec.auth.oidc.caFileRef mounts) and stays on disk for the virtual
# workspaces' --oidc-ca-file and `contribctl tenancy login --ca-file`.
# Reused across runs, and recovered from the cluster when this directory is
# gone, because a CHANGED certificate is expensive here in a way a missing
# one is not:
#
#   - a browser will not talk to a self-signed issuer until someone has
#     visited it once and accepted the warning, and that acceptance is per
#     certificate — so a new one silently breaks signing in from a browser;
#   - dex reads its certificate once at boot, so a new one means every OIDC
#     validator has to be rolled with it or they answer 401 to everything.
#
# So the cluster's secret is the source of truth and this directory is a
# cache of it. Wiping .tilt no longer changes the certificate; only losing
# the cluster does.

set -euo pipefail

DEX_DIR="${1:?usage: dex-pki.sh <dir>}"
VALID_DAYS="${PKI_VALID_DAYS:-365}"
DEX_HOST="${DEX_HOST:-dex.kcp.localhost}"
SECRET="${DEX_TLS_SECRET:-dex-tls}"

mkdir -p "${DEX_DIR}"
if openssl x509 -in "${DEX_DIR}/tls.crt" -checkend 3600 >/dev/null 2>&1; then
  echo "dex serving cert in ${DEX_DIR} is current"
  exit 0
fi

# Nothing usable on disk. Before minting a new one — which invalidates
# every browser exception and requires rolling the validators — see whether
# the cluster already has one.
recover() {
  local key crt
  key="$(kubectl get secret "${SECRET}" -o jsonpath='{.data.tls\.key}' 2>/dev/null || true)"
  crt="$(kubectl get secret "${SECRET}" -o jsonpath='{.data.tls\.crt}' 2>/dev/null || true)"
  [ -n "${key}" ] && [ -n "${crt}" ] || return 1

  printf '%s' "${crt}" | base64 -d > "${DEX_DIR}/tls.crt.try" 2>/dev/null || return 1
  printf '%s' "${key}" | base64 -d > "${DEX_DIR}/tls.key.try" 2>/dev/null || return 1
  openssl x509 -in "${DEX_DIR}/tls.crt.try" -checkend 3600 >/dev/null 2>&1 || return 1

  mv "${DEX_DIR}/tls.crt.try" "${DEX_DIR}/tls.crt"
  mv "${DEX_DIR}/tls.key.try" "${DEX_DIR}/tls.key"
  chmod 600 "${DEX_DIR}/tls.key"
  cp "${DEX_DIR}/tls.crt" "${DEX_DIR}/ca.crt"
  return 0
}

if recover; then
  echo "recovered the dex serving cert from secret/${SECRET}; nothing to re-accept"
  exit 0
fi
rm -f "${DEX_DIR}"/tls.crt.try "${DEX_DIR}"/tls.key.try

echo "generating dex serving cert in ${DEX_DIR} (valid ${VALID_DAYS} days)"
echo "NOTE: a new certificate — accept it once in the browser, and the"
echo "      validators roll with it automatically."
rm -f "${DEX_DIR}"/tls.crt "${DEX_DIR}"/tls.key "${DEX_DIR}"/ca.crt

openssl req -x509 -newkey rsa:2048 -nodes -days "${VALID_DAYS}" \
  -keyout "${DEX_DIR}/tls.key" -out "${DEX_DIR}/tls.crt" \
  -subj "/CN=${DEX_HOST}" \
  -addext "subjectAltName=DNS:${DEX_HOST}"

# Self-signed: the leaf is its own trust anchor.
cp "${DEX_DIR}/tls.crt" "${DEX_DIR}/ca.crt"
