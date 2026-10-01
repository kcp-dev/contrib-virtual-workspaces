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

# Keeps https://kcp.localhost:8443 reachable from the host.
#
# The gateway's Service is a LoadBalancer whose external IP is only routable
# inside the cluster, so the host reaches it through a port-forward. That
# forward used to be something you started by hand, which meant it died with
# the gateway pod and took the whole stack's address with it — every
# kubeconfig, the UI, and the CLI all point at this one port, so losing it
# looks like everything being broken at once.
#
# Run as a Tilt serve_cmd it is restarted automatically, and this loop
# re-resolves the Service each time: its name carries a hash that changes
# when the Gateway is recreated, so hard-coding it would break on exactly
# the rebuild this is meant to survive.

set -uo pipefail

NAMESPACE="${GATEWAY_NAMESPACE:-envoy-gateway-system}"
SELECTOR="${GATEWAY_SELECTOR:-gateway.envoyproxy.io/owning-gateway-name=eg}"
PORT="${INGRESS_PORT:-8443}"

while :; do
  svc="$(kubectl -n "$NAMESPACE" get svc -l "$SELECTOR" \
    -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)"

  if [ -z "$svc" ]; then
    echo "waiting for the gateway Service in $NAMESPACE ($SELECTOR)"
    sleep 5
    continue
  fi

  echo "forwarding localhost:$PORT -> $NAMESPACE/$svc:$PORT"
  # Exits when the pod behind the Service goes away; the loop re-resolves
  # and reconnects rather than leaving the port dead.
  kubectl -n "$NAMESPACE" port-forward "svc/$svc" "$PORT:$PORT" || true
  echo "forward dropped, reconnecting"
  sleep 2
done
