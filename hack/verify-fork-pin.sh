#!/usr/bin/env bash

# Copyright 2026 The KCP Authors.
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

# virtual-workspace-framework builds against kcp's fork of Kubernetes, and Go
# does not propagate replace directives across module boundaries. This module
# therefore mirrors the framework's replace block, and the two pins must match
# exactly: a mismatch produces build failures about undefined symbols in
# k8s.io/apiserver (for example request.Cluster), which are easy to misread as
# a code problem rather than a dependency one.
#
# This check compares our k8s.io replace pin against the framework's.

set -o errexit
set -o nounset
set -o pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${REPO_ROOT}"

vwf_version="$(go list -m -f '{{.Version}}' github.com/kcp-dev/virtual-workspace-framework)"
vwf_dir="$(go list -m -f '{{.Dir}}' github.com/kcp-dev/virtual-workspace-framework)"

extract_pin() {
	# Prints the fork version used for k8s.io/apiserver, which stands in for the
	# whole staging set — they are always bumped together.
	grep -E '^[[:space:]]*k8s\.io/apiserver => github\.com/kcp-dev/kubernetes' "$1" \
		| awk '{print $NF}'
}

ours="$(extract_pin go.mod)"
theirs="$(extract_pin "${vwf_dir}/go.mod")"

if [[ -z "${ours}" ]]; then
	echo "ERROR: go.mod has no replace directive for k8s.io/apiserver." >&2
	echo "This module must mirror the kcp Kubernetes fork replace block from" >&2
	echo "virtual-workspace-framework ${vwf_version}." >&2
	exit 1
fi

if [[ -z "${theirs}" ]]; then
	echo "ERROR: could not read the fork pin from virtual-workspace-framework ${vwf_version}." >&2
	exit 1
fi

if [[ "${ours}" != "${theirs}" ]]; then
	echo "ERROR: kcp Kubernetes fork pin does not match virtual-workspace-framework ${vwf_version}." >&2
	echo "  ours:   ${ours}" >&2
	echo "  theirs: ${theirs}" >&2
	echo >&2
	echo "Update the replace block in go.mod to ${theirs} (all k8s.io entries)," >&2
	echo "then run 'go mod tidy'." >&2
	exit 1
fi

echo "kcp Kubernetes fork pin matches virtual-workspace-framework ${vwf_version} (${ours})"
