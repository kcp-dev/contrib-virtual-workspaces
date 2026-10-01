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

# Regenerates every APIResourceSchema from the Go types, the way kcp does
# it: controller-gen renders CRDs from the kubebuilder markers, kcp's
# apigen converts them into APIResourceSchemas — content-addressed names
# (v<date>-<git sha>.<plural>.<group>), reusing the committed schema when
# nothing changed, because APIResourceSchemas are immutable and a changed
# schema MUST be a new object — and this script then rewrites the schema
# references inside each component's own APIExport manifests.
#
# apigen also wants to own one APIExport per group, which this repo
# deliberately does not do (a group's resources are split across exports
# so that capabilities are granted separately). Its export output is
# discarded; only the schemas and their names are taken.
#
# Run `make generate` after changing any sdk/apis type.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${REPO_ROOT}"

CONTROLLER_GEN_VERSION="${CONTROLLER_GEN_VERSION:-v0.19.0}"

log() { printf '\033[1;32m==>\033[0m %s\n' "$*"; }

# component <name> <types package> <assets dir> <virtual resources to skip...>
generate() {
  local name=$1 pkg=$2 assets=$3
  shift 3
  local stage
  stage="$(mktemp -d)"
  mkdir -p "${stage}/crds" "${stage}/out"

  log "${name}: rendering CRDs from ${pkg}"
  go run "sigs.k8s.io/controller-tools/cmd/controller-gen@${CONTROLLER_GEN_VERSION}" \
    crd paths="${pkg}" output:crd:dir="${stage}/crds"

  # Virtual kinds are served by the virtual workspace, never stored in
  # kcp, so they get no APIResourceSchema.
  local virtual
  for virtual in "$@"; do
    rm -f "${stage}/crds/"*_"${virtual}.yaml"
  done

  # Seed the stage with the committed GENERATED schemas so apigen can keep
  # their names when the shape did not change. (Only files following its
  # apiresourceschema-<plural>.<group>.yaml naming; hand-written ones from
  # before generation cannot seed it and are simply replaced.)
  cp "${assets}"/apiresourceschema-*.*.*.yaml "${stage}/out/" 2>/dev/null || true

  log "${name}: converting CRDs to APIResourceSchemas"
  go run github.com/kcp-dev/sdk/cmd/apigen \
    --input-dir "${stage}/crds" --output-dir "${stage}/out"

  # Take the schemas, drop apigen's per-group APIExport, prune what it no
  # longer generates, and rewrite our exports' schema references.
  python3 - "$name" "$assets" "$stage/out" <<'PY'
import glob, os, re, sys

name, assets, out = sys.argv[1], sys.argv[2], sys.argv[3]

generated = {}  # (group, plural) -> (schema name, file basename)
for path in sorted(glob.glob(os.path.join(out, "apiresourceschema-*.yaml"))):
    text = open(path).read()
    schema_name = re.search(r"^  name: (\S+)$", text, re.M).group(1)
    group = re.search(r"^  group: (\S+)$", text, re.M).group(1)
    plural = re.search(r"^    plural: (\S+)$", text, re.M).group(1)
    generated[(group, plural)] = (schema_name, os.path.basename(path))

for old in glob.glob(os.path.join(assets, "apiresourceschema-*.yaml")):
    os.remove(old)
for (_, _), (schema_name, base) in generated.items():
    src = os.path.join(out, base)
    dst = os.path.join(assets, base)
    open(dst, "w").write(open(src).read())

# Rewrite `schema: <old>` lines in OUR APIExport manifests: any name
# ending in .<plural>.<group> becomes the freshly generated one.
for path in glob.glob(os.path.join(assets, "apiexport-*.yaml")):
    text = open(path).read()
    for (group, plural), (schema_name, _) in generated.items():
        text = re.sub(
            r"(schema: )\S*\.%s\.%s\b" % (re.escape(plural), re.escape(group)),
            r"\g<1>" + schema_name, text)
    open(path, "w").write(text)

names = ", ".join(sorted(v[1] for v in generated.values()))
print(f"{name}: {len(generated)} schemas -> {names}")
PY

  rm -rf "${stage}"
}

generate marketplace ./marketplace/sdk/apis/marketplace/v1alpha1 marketplace/config/apiexport catalogofferings
generate mcp ./mcp/sdk/apis/mcp/v1alpha1 mcp/config/apiexport
# The self-* kinds are the virtual workspace's own request surface: they
# are answered in memory and never stored, so they need no schema and must
# not appear in the export.
generate tenancy ./tenancy/sdk/apis/tenancy/v1alpha1 tenancy/config/apiexport \
  selftenancyreviews selfprojectrequests selftenantrequests

log "done; if schema file names changed, update the ExportsOrder lists in"
log "  marketplace/config/apiexport/bootstrap.go and tenancy/config/apiexport/bootstrap.go"
