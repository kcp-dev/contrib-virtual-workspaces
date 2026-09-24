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

# Seeds the Tilt environment with demo tenancy data, idempotently: a
# fresh user has nothing to see otherwise (whoami: "not a member of any
# tenant"). Everything here is if-not-present / role-upserting, so
# re-running (tilt trigger tenancy-seed) is always safe.
#
#   tenant  acme        with project web
#   admin of acme:      alice@team-a.example.com (dex JWT) and alice (dev cert)
#   view on acme/web:   bob@team-b.example.com   (dex JWT) and bob   (dev cert)

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
KUBECONFIG_FILE="${1:-${REPO_ROOT}/.tilt/tilt-frontproxy.kubeconfig}"
CLI="${REPO_ROOT}/bin/contribctl"

tenancy() { "${CLI}" tenancy "$@" --kubeconfig "${KUBECONFIG_FILE}"; }

tenancy tenant create acme --display-name "Acme Corp" --if-not-present
tenancy project create web --tenant acme --if-not-present

for subject in alice@team-a.example.com alice; do
  tenancy grant "${subject}" --tenant acme --role admin
done
for subject in bob@team-b.example.com bob; do
  tenancy grant "${subject}" --tenant acme --project web --role view
done

echo "seeded: tenant acme (project web); alice=admin, bob=view on web"
