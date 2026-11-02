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

# Publishes MCP to the marketplace catalog.
#
# MCP is its own producer: `mcp init` already installed its APIExport in
# root:mcp:controllers, annotated for the catalog. Two things are still
# missing before the marketplace will list it, and neither belongs to mcp:
#
#   1. a Producer record naming that workspace, because the marketplace
#      refuses to publish from a workspace no record names — otherwise any
#      workspace could put itself on the catalog by annotating its own
#      export;
#   2. a marketplace-publish binding in that workspace, which is what lets
#      the marketplace operator see the annotation and materialize the
#      `bind` grant. Binding it IS the registration consent.
#
# That makes mcp an external ("bring your own") producer in marketplace
# terms, which is the honest description: it is a component in this repo,
# but to the marketplace it is just a workspace that asked to publish.

set -euo pipefail

KUBECONFIG_FILE="${KUBECONFIG:?KUBECONFIG must point at a kcp admin kubeconfig}"
SERVER="${KCP_FRONT_PROXY_URL:-https://kcp.localhost:8443}"
CATALOG="${MARKETPLACE_CATALOG_WORKSPACE:-root:marketplace:catalog}"
EXPORTS="${MARKETPLACE_EXPORTS_WORKSPACE:-root:marketplace:controllers}"
MCP_WS="${MCP_WORKSPACE:-root:mcp:controllers}"
TIMEOUT="${TIMEOUT_SECONDS:-120}"

kube() { kubectl --kubeconfig "$KUBECONFIG_FILE" --server "$1" "${@:2}"; }

echo "==> registering $MCP_WS as a marketplace producer"
kube "$SERVER/clusters/$CATALOG" apply -f - <<EOF
apiVersion: marketplace.platform.kcp.io/v1alpha1
kind: Producer
metadata:
  name: mcp
spec:
  displayName: MCP
  description: The Model Context Protocol, as a kcp virtual workspace.
  mode: external
  workspacePath: $MCP_WS
EOF

# The claims this binding accepts are what the marketplace operator uses
# to read the annotated export and write the bind grant next to it. Spelled
# out because a hand-made binding accepts nothing by default — the
# WorkspaceType path that usually does this does not apply to a workspace
# somebody else already created.
echo "==> binding marketplace-publish in $MCP_WS (the registration consent)"
kube "$SERVER/clusters/$MCP_WS" apply -f - <<EOF
apiVersion: apis.kcp.io/v1alpha2
kind: APIBinding
metadata:
  name: marketplace-publish
spec:
  reference:
    export:
      path: $EXPORTS
      name: marketplace-publish
  permissionClaims:
    - group: apis.kcp.io
      resource: apiexports
      verbs: [get, list, watch]
      state: Accepted
      selector: {matchAll: true}
    - group: apis.kcp.io
      resource: apiexportendpointslices
      verbs: [get, list, watch]
      state: Accepted
      selector: {matchAll: true}
    - group: core.kcp.io
      resource: logicalclusters
      verbs: [get, list, watch]
      state: Accepted
      selector: {matchAll: true}
    - group: ""
      resource: configmaps
      verbs: [get, list, watch]
      state: Accepted
      selector: {matchAll: true}
    - group: rbac.authorization.k8s.io
      resource: clusterroles
      verbs: [get, list, watch, create, update, delete]
      state: Accepted
      selector: {matchAll: true}
    - group: rbac.authorization.k8s.io
      resource: clusterrolebindings
      verbs: [get, list, watch, create, update, delete]
      state: Accepted
      selector: {matchAll: true}
EOF

# Publishing materializes exactly one thing, and it is the only observable
# effect: `bind` on the mcp export. No grant, nobody can enable it.
echo "==> waiting for the bind grant"
deadline=$((SECONDS + TIMEOUT))
until kube "$SERVER/clusters/$MCP_WS" get clusterrole marketplace:bind:mcp >/dev/null 2>&1; do
  if [ "$SECONDS" -ge "$deadline" ]; then
    echo "no bind grant for the mcp export after ${TIMEOUT}s." >&2
    echo "Is marketplace-operator running, and did the Producer record resolve?" >&2
    exit 1
  fi
  sleep 2
done

echo
echo "MCP is on the catalog. Enable it in a project:"
echo "  bin/contribctl offers"
echo "  bin/contribctl enable mcp.mcp"
echo
echo "Then read back where to point a client:"
echo "  kubectl --server $SERVER/clusters/<project> get mcpendpoints"
