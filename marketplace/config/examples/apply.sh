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

# Publishes the example offerings into a running marketplace.
#
# Two workspaces are involved and they are not interchangeable, which is
# why this is a script rather than one `kubectl apply -f`: the Producer
# record is registration and belongs to the catalog, while the exports are
# the offerings themselves and belong to the producer's own workspace —
# which does not exist until the operator has read the record.
#
# Idempotent: re-running re-applies, so editing an example and triggering
# it again is the development loop.

set -euo pipefail

KUBECONFIG_FILE="${KUBECONFIG:?KUBECONFIG must point at a kcp admin kubeconfig}"
SERVER="${KCP_FRONT_PROXY_URL:-https://kcp.localhost:8443}"
CATALOG="${MARKETPLACE_CATALOG_WORKSPACE:-root:marketplace:catalog}"
TIMEOUT="${TIMEOUT_SECONDS:-120}"
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

kube() { kubectl --kubeconfig "$KUBECONFIG_FILE" --server "$1" "${@:2}"; }

echo "==> registering producer acme in $CATALOG"
kube "$SERVER/clusters/$CATALOG" apply -f "$HERE/producer.yaml"

# The operator provisions the workspace and reports its path back on the
# record. Waiting for the path rather than for a phase is deliberate: the
# path is the thing the next step actually needs.
echo "==> waiting for the operator to provision acme's workspace"
deadline=$((SECONDS + TIMEOUT))
while :; do
  path="$(kube "$SERVER/clusters/$CATALOG" get producer acme \
    -o jsonpath='{.status.workspacePath}' 2>/dev/null || true)"
  phase="$(kube "$SERVER/clusters/$CATALOG" get producer acme \
    -o jsonpath='{.status.phase}' 2>/dev/null || true)"
  [ -n "$path" ] && break
  if [ "$SECONDS" -ge "$deadline" ]; then
    echo "producer acme has no workspace after ${TIMEOUT}s (phase: ${phase:-none})." >&2
    echo "Is marketplace-operator running?" >&2
    exit 1
  fi
  sleep 2
done
echo "    $path"

PROVIDER_WS="$SERVER/clusters/$path"

# The APIExport references a schema by name, so the schema has to land
# first; both files carry both, in that order.
for example in things things-instructions widgets; do
  echo "==> publishing $example into $path"
  kube "$PROVIDER_WS" apply -f "$HERE/$example.yaml"
done

# Publishing materializes exactly one thing, and it is the only observable
# effect there is to wait for: `bind` on that export. No annotation, no
# grant; no grant, nobody can enable it.
echo "==> waiting for the bind grants"
deadline=$((SECONDS + TIMEOUT))
for export_name in things.acme.example widgets.acme.example; do
  while ! kube "$PROVIDER_WS" get clusterrole "marketplace:bind:$export_name" >/dev/null 2>&1; do
    if [ "$SECONDS" -ge "$deadline" ]; then
      echo "no bind grant for $export_name after ${TIMEOUT}s." >&2
      echo "The operator only grants it for a workspace a Producer record names." >&2
      exit 1
    fi
    sleep 2
  done
  echo "    marketplace:bind:$export_name"
done

echo
echo "published. browse the catalog with:"
echo "  kubectl --kubeconfig $KUBECONFIG_FILE get --raw \\"
echo "    $SERVER/services/marketplace/apis/marketplace.platform.kcp.io/v1alpha1/offerings"
echo
echo "or enable one as yourself:"
echo "  bin/contribctl offers"
echo "  bin/contribctl enable acme.things.acme.example"
