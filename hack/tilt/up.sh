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

# Brings up the whole development stack: a kind cluster (the same name
# kcp's own tilt setup uses, so either entry point works on it), then Tilt
# with the repo Tiltfile — kcp from the kcp checkout, this repo's
# components on top.
#
#   hack/tilt/up.sh
#   KCP_DIR=/path/to/kcp hack/tilt/up.sh

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${REPO_ROOT}"

for tool in tilt kind helm kubectl docker; do
  command -v "${tool}" >/dev/null 2>&1 || { echo "ERR: ${tool} is required"; exit 1; }
done

KCP_DIR="${KCP_DIR:-${REPO_ROOT}/../kcp}"
[[ -f "${KCP_DIR}/contrib/tilt/Tiltfile" ]] || { echo "ERR: no kcp checkout at ${KCP_DIR}; set KCP_DIR"; exit 1; }

CLUSTER_NAME="${CLUSTER_NAME:-kcp-tilt}"
if ! kind get clusters 2>/dev/null | grep -w -q "${CLUSTER_NAME}"; then
  kind create cluster --name "${CLUSTER_NAME}"
fi
kubectl config use-context "kind-${CLUSTER_NAME}"

KCP_DIR="${KCP_DIR}" exec tilt up "$@"
