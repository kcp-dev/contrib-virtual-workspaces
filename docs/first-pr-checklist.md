# First PR into contrib-access-virtual-workspace — what's needed

Audit of the repo as it stands against what a `kcp-dev` out-of-tree repo needs.

## Already in place

The Go code is the post-review version — `graph.SetEndpoint` for endpoint refresh, the
`clusterLifecycle` handler calling `ForgetCluster`, `typedFromEvent`, `ready` as an
`atomic.Bool`, `errors.Join` in options validation. Imports are rewritten to
`github.com/kcp-dev/contrib-access-virtual-workspace`, no stale `cnvergence` paths.

Dependencies are pinned standalone — k8s 0.36.3, `virtual-workspace-framework` v0.32.3,
`sdk` v0.32.3, controller-runtime and multicluster-runtime 0.24.1,
multicluster-provider v0.8.0 — with `go.sum` committed. No relative replaces needed now
that we are out of tree, which removes the pin-mirroring problem that pushed us toward
in-tree in the first place.

21 of 22 Go files carry the Apache header; `.gitignore` and `Dockerfile` exist.

## Blocking for the first PR

**Repo hygiene.** kcp-dev repos carry `LICENSE`, `OWNERS`, `CONTRIBUTING.md`, `DCO`,
`SECURITY.md`, `code-of-conduct.md`. `OWNERS` is functionally required — Prow needs it for
`/lgtm` and `/approve`. Everything else can be copied from kcp with the names changed.

**License header on `pkg/generated/openapi/zz_generated.openapi.go`.** The one file
missing it. If the generator does not emit the boilerplate, the codegen script has to
prepend it, because a `verify-boilerplate` job will fail on it.

**Makefile.** `build`, `test`, `lint`, `verify`, `codegen`, `image`, and the `init` target
the reviewer asked for. There is no Makefile at all right now, and the README's build
instructions have nothing to point at.

**CI.** Nothing runs today. Minimum: build, unit tests, `gofmt`/lint, `go mod tidy`
verification, and codegen verification (deepcopy and openapi are committed generated
files, so drift needs catching). Whether that is GitHub Actions or a `.prow.yaml` depends
on how kcp-dev wires new repos — worth asking, since kcp itself uses Prow.

**Codegen scripts.** `zz_generated_deepcopy.go`, `zz_generated.openapi.go` and
`config/apiexport/apiresourceschema.yaml` are all generated but there is no script to
regenerate them. Needs `hack/update-codegen.sh` (deepcopy-gen, openapi-gen, and kcp's
`apigen` for the APIResourceSchema) plus a verify counterpart.

**Consolidate the two READMEs.** `README.md` is tracked and `docs/README.md` duplicates
it. Root README stays; move deployment detail into `docs/`.

## Blocking, from review feedback

**`init` subcommand and embedded config.** Follow kcp's pattern: `//go:embed *.yaml` plus
`confighelpers.Bootstrap`, as `config/root/bootstrap.go` does. Two modes — a privileged
user passing `--workspace=:root:access:magic` where init creates missing workspaces along
the path (default `:root:access`), and an unprivileged user with cluster-admin in one
unknown workspace where init bootstraps into whatever the kubeconfig context points at.
Idempotent, so it can run as an initContainer. It should also *verify*: APIExport ready,
endpointslice populated, permission claims accepted — the checks that would have caught
the empty-graph report instead of the server saying `ready: true` over nothing.

This has a knock-on effect on flags. `spec.reference.export.path: root` in the example
APIBinding only holds if the export lives in root, and `--apiexport-endpointslice=<name>`
is ambiguous once the export can live anywhere. Both need to become workspace-qualified.

**JWT authentication.** Only `DelegatingAuthenticationOptions` is wired, which means
bearer tokens depend on kcp serving `TokenReview` — unverified, and probably not the case.
Clients hitting the VW directly present an OIDC token nobody has validated. Needs
`--authentication-config` (structured `AuthenticationConfiguration`, same file kcp uses)
with `--oidc-*` flags as a fallback, so the username and group prefixes match kcp's
exactly and the graph's string comparison holds.

**Diagnostics.** Log the resolved username and groups, log the endpoint URLs the
endpointslice resolves to at startup, and expose the engaged-cluster count in
`/debug/graph`. Without these, "empty graph" and "misconfigured" look identical from
outside.

## Can follow later

Release workflow publishing to `ghcr.io/kcp-dev/...` — being out of tree, the image is now
our responsibility rather than something to negotiate with kcp maintainers, so it needs to
exist, but not before the code lands. Likewise e2e tests against a kind-based kcp, and the
`hack/kind` harness from the PoC once it is reworked. `cmd/scar-to-kubeconfig` is in the
tree already; the reviewer's PoC renamed it `scarctl`, so pick one name.

## Also worth doing

Delete the `contrib/access-vw` copy on the kcp branch, so there is one source of truth.

The out-of-tree decision resolves two open questions from the earlier draft PR
description: it now matches what #3839 actually asked for, so the placement deviation
disappears, and the image question is answered — we own it. The PR description needs
rewriting on both points.
