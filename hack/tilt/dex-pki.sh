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
# Reused across runs; regenerated when missing or close to expiry.

set -euo pipefail

DEX_DIR="${1:?usage: dex-pki.sh <dir>}"
VALID_DAYS="${PKI_VALID_DAYS:-30}"
DEX_HOST="${DEX_HOST:-dex.kcp.localhost}"

mkdir -p "${DEX_DIR}"
if openssl x509 -in "${DEX_DIR}/tls.crt" -checkend 3600 >/dev/null 2>&1; then
  echo "dex serving cert in ${DEX_DIR} is current"
  exit 0
fi

echo "generating dex serving cert in ${DEX_DIR} (valid ${VALID_DAYS} days)"
rm -f "${DEX_DIR}"/tls.crt "${DEX_DIR}"/tls.key "${DEX_DIR}"/ca.crt

openssl req -x509 -newkey rsa:2048 -nodes -days "${VALID_DAYS}" \
  -keyout "${DEX_DIR}/tls.key" -out "${DEX_DIR}/tls.crt" \
  -subj "/CN=${DEX_HOST}" \
  -addext "subjectAltName=DNS:${DEX_HOST}"

# Self-signed: the leaf is its own trust anchor.
cp "${DEX_DIR}/tls.crt" "${DEX_DIR}/ca.crt"
