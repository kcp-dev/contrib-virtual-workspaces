#!/usr/bin/env bash
# Regenerates everything derived from apis/: deepcopy methods, and the
# APIResourceSchema in config/ that kcp serves — and repoints the APIExport
# at the schema's new name when the shape changed.
#
# A schema name carries a hash of the shape (v<date>-<hash>.<plural>.<group>),
# so a changed API is a new schema and the export's reference has to follow;
# that is how kcp knows an existing binding is on an older revision.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${HERE}"

CONTROLLER_GEN_VERSION="${CONTROLLER_GEN_VERSION:-v0.19.0}"
stage="$(mktemp -d)"
trap 'rm -rf "${stage}"' EXIT
mkdir -p "${stage}/crds" "${stage}/out"

echo "==> deepcopy"
go run "sigs.k8s.io/controller-tools/cmd/controller-gen@${CONTROLLER_GEN_VERSION}" \
  object paths=./apis/...

echo "==> CRDs (intermediate; kcp serves APIResourceSchemas, not CRDs)"
go run "sigs.k8s.io/controller-tools/cmd/controller-gen@${CONTROLLER_GEN_VERSION}" \
  crd paths=./apis/... output:crd:dir="${stage}/crds"

# Seed with the committed schema so apigen keeps its name when nothing
# changed — a stable name means existing bindings stay current.
cp config/apiresourceschema-*.yaml "${stage}/out/" 2>/dev/null || true

echo "==> APIResourceSchemas"
go run github.com/kcp-dev/sdk/cmd/apigen --input-dir "${stage}/crds" --output-dir "${stage}/out"

python3 - "${stage}/out" config <<'PY'
import glob, os, re, sys
out, cfg = sys.argv[1], sys.argv[2]
for old in glob.glob(os.path.join(cfg, "apiresourceschema-*.yaml")):
    os.remove(old)
export = open(os.path.join(cfg, "apiexport.yaml")).read()
for path in sorted(glob.glob(os.path.join(out, "apiresourceschema-*.yaml"))):
    text = open(path).read()
    name = re.search(r"^  name: (\S+)$", text, re.M).group(1)
    _, plural_group = name.split(".", 1)
    open(os.path.join(cfg, os.path.basename(path)), "w").write(text)
    # schema: <anything>.<plural>.<group>  ->  the new name
    export, n = re.subn(r"schema: \S+\." + re.escape(plural_group) + r"$", "schema: " + name, export, flags=re.M)
    print("    %s%s" % (name, "" if n else "  (no export entry references it)"))
open(os.path.join(cfg, "apiexport.yaml"), "w").write(export)
PY
echo "==> done"
