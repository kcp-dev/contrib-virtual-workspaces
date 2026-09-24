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

# Mints the development client CA and user certificates the virtual
# workspaces authenticate direct callers with under Tilt: CN becomes the
# username, O the groups. Reused across runs; regenerated when missing or
# close to expiry.

set -euo pipefail

PKI_DIR="${1:?usage: pki.sh <dir>}"
VALID_DAYS="${PKI_VALID_DAYS:-30}"

mkdir -p "${PKI_DIR}"
if [[ -f "${PKI_DIR}/alice.crt" ]] && openssl x509 -in "${PKI_DIR}/ca.crt" -checkend 3600 >/dev/null 2>&1; then
  echo "dev PKI in ${PKI_DIR} is current"
  exit 0
fi

echo "generating dev PKI in ${PKI_DIR} (valid ${VALID_DAYS} days)"
rm -f "${PKI_DIR}"/*.crt "${PKI_DIR}"/*.key "${PKI_DIR}"/*.csr "${PKI_DIR}"/*.srl

openssl req -x509 -newkey rsa:2048 -nodes -days "${VALID_DAYS}" \
  -keyout "${PKI_DIR}/ca.key" -out "${PKI_DIR}/ca.crt" \
  -subj "/CN=contrib-tilt-dev-ca"

for spec in "alice:team-a" "bob:team-b"; do
  user="${spec%%:*}"; group="${spec#*:}"
  openssl req -newkey rsa:2048 -nodes \
    -keyout "${PKI_DIR}/${user}.key" -out "${PKI_DIR}/${user}.csr" \
    -subj "/CN=${user}/O=${group}"
  openssl x509 -req -in "${PKI_DIR}/${user}.csr" \
    -CA "${PKI_DIR}/ca.crt" -CAkey "${PKI_DIR}/ca.key" -CAcreateserial \
    -days "${VALID_DAYS}" -out "${PKI_DIR}/${user}.crt"
done
