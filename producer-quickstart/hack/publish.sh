#!/usr/bin/env bash
# Publishes this producer to a marketplace: registers the Producer record,
# waits for its workspace, applies the schema, export, endpoint slice and
# instructions there, and waits for the catalog's `bind` grant — the one
# observable effect of being published.
#
# Needs an admin kubeconfig for kcp in KUBECONFIG. The marketplace operator
# provisions the workspace; nothing here creates one.
set -euo pipefail

KUBECONFIG_FILE="${KUBECONFIG:?KUBECONFIG must point at a kcp admin kubeconfig}"
SERVER="${KCP_FRONT_PROXY_URL:-https://kcp.localhost:8443}"
CATALOG="${MARKETPLACE_CATALOG_WORKSPACE:-root:marketplace:catalog}"
TIMEOUT="${TIMEOUT_SECONDS:-120}"
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

kube() { kubectl --kubeconfig "$KUBECONFIG_FILE" --server "$1" "${@:2}"; }

producer="$(sed -n 's/^  name: //p' "$HERE/config/producer.yaml" | head -1)"
export_name="$(sed -n 's/^  name: //p' "$HERE/config/apiexport.yaml" | head -1)"

echo "==> registering producer $producer in $CATALOG"
kube "$SERVER/clusters/$CATALOG" apply -f "$HERE/config/producer.yaml"

echo "==> waiting for the operator to provision its workspace"
deadline=$((SECONDS + TIMEOUT))
while :; do
  path="$(kube "$SERVER/clusters/$CATALOG" get producer "$producer" -o jsonpath='{.status.workspacePath}' 2>/dev/null || true)"
  [ -n "$path" ] && break
  if [ "$SECONDS" -ge "$deadline" ]; then
    echo "producer $producer has no workspace after ${TIMEOUT}s. Is marketplace-operator running?" >&2
    exit 1
  fi
  sleep 2
done
echo "    $path"
WS="$SERVER/clusters/$path"

echo "==> publishing into $path"
kube "$WS" apply -f "$HERE"/config/apiresourceschema-*.yaml
kube "$WS" apply -f "$HERE/config/apiexport.yaml"
kube "$WS" apply -f "$HERE/config/instructions.yaml"
# The slice names the export by workspace path, known only now.
sed "s#__PRODUCER_WORKSPACE__#$path#" "$HERE/config/apiexportendpointslice.yaml" | kube "$WS" apply -f -

echo "==> waiting for the bind grant"
deadline=$((SECONDS + TIMEOUT))
while ! kube "$WS" get clusterrole "marketplace:bind:$export_name" >/dev/null 2>&1; do
  if [ "$SECONDS" -ge "$deadline" ]; then
    echo "no bind grant for $export_name after ${TIMEOUT}s; is the export annotated with publish: \"true\"?" >&2
    exit 1
  fi
  sleep 2
done

cat <<EOF

published. run the controller against it with:

  bin/producer --kubeconfig $KUBECONFIG_FILE --workspace-path $path

and enable it in a project from the console, or with:

  contribctl enable $producer.$export_name
EOF
