# MCP Virtual Workspace

Serves the [Model Context Protocol](https://modelcontextprotocol.io) as a kcp
virtual workspace, scoped to what the calling user can actually see.

An agent connected to a kcp installation needs two things: a list of workspaces
it may work on, and a way to act in them as the human who asked. This component
provides the second. The first comes from
[contrib-access-virtual-workspace](https://github.com/kcp-dev/contrib-access-virtual-workspace),
which answers `SelfClusterAccessReview`.

See [kcp-dev/kcp#3839](https://github.com/kcp-dev/kcp/issues/3839).

## How it works

Registered as a raw-handler virtual workspace at `/services/mcp`. Raw rather
than a delegated apiserver because MCP is JSON-RPC over streamable HTTP — there
is no group, version or resource for the fixed-group-version machinery to serve.
It still sits behind the root apiserver's filter chain, so authentication,
authorization and audit are the framework's rather than hand-rolled.

Two things happen per request: the caller's workspace list is fetched from the
access virtual workspace and cached briefly, and per-workspace API calls are made
with this server's own credential impersonating the caller. Both deserve
explanation.

## Layout

| Path | Description |
|------|-------------|
| `cmd` | cobra commands: `serve`. |
| `internal/config` | flag surface. |
| `internal/access` | client for the access virtual workspace, with the per-caller cache. |
| `internal/mcp` | the virtual workspace: serving, authentication, per-caller scope. |
| `pkg/tools` | the shared kit MCP tools, one package per toolset, each exposing `Register`.  |
| `pkg/toolsets` | maps toolset names to `Register` functions; owns validation and the default selection. |
| `deploy/helm` | chart. |
| `deploy/kcp` | front-proxy path mapping and the RBAC this server's identity needs. |
| `test/e2e` | deploys the real thing into kind and speaks MCP to it. |

## Running

```sh
make build

bin/mcp-virtual-workspace serve \
  --kubeconfig=$KUBECONFIG \
  --access-url=https://localhost:9443/services/access \
  --secure-port=9444 \
  --authentication-config=/path/to/kcp/authentication-config.yaml
```

Serving is TLS-only; a self-signed certificate is generated for development when
none is supplied. At least one authentication method is required — the server
refuses to start otherwise, because every caller would be anonymous and the
symptom would be an empty tool list rather than an error.

The authentication configuration must match kcp's. Prefer
`--authentication-config` pointing at the same `AuthenticationConfiguration` file
kcp is started with; the `--oidc-*` flags work too but have to be kept in step by
hand.

## Testing

`make test` is unit tests. `make test-e2e` is the real thing:

```sh
make test-e2e          # tears the cluster down afterwards
make test-e2e-keep     # keeps it for inspection (NO_TEARDOWN=true)
```

It needs `kind`, `kubectl`, `helm`, `docker`, `go` and `git`. The script creates
a kind cluster, installs cert-manager and kcp-operator, builds this image from
the working tree and the access virtual workspace from its main branch, and then
deploys kcp, both virtual workspaces and two consumer workspaces.

It then asks `list_kcp_workspaces` over MCP twice, with two credentials that have
opposite rights: a client certificate for `alice`, and a bearer token for
`agent` from the front-proxy's static token file. Each must see its own
workspace and not the other's — expectations that only both hold if the answer
followed the caller.

That covers the parts no unit test can: kcp-operator rendering a Deployment this
binary accepts, the front-proxy routing `/services/mcp` and turning either
credential into the identity headers this server trusts, and the impersonated
round trip to the access virtual workspace.

Not covered: callers that reach this server directly with a JWT, i.e.
`--authentication-config` and the `--oidc-*` flags. In e2e the front-proxy is
always the one validating credentials, and this server only ever authenticates
`X-Remote-*` headers.

Both dependencies track a moving branch, so the job can go red without anything
here having changed — which is the point. Pin them with `KCP_OPERATOR_REF` and
`ACCESS_VW_REF`, or build the access virtual workspace from a local checkout
with `ACCESS_VW_SRC=../contrib-access-virtual-workspace`. The script's header
lists every knob.

One such red is outstanding: the access virtual workspace needs the revision
where `access-vw-init` merges its assets instead of replacing them. Before it,
init creates the `APIExportEndpointSlice` and can never re-apply it — kcp
defaults `spec.export.path` and rejects the update as a changed reference — so
any restart of that init container is a permanent crash loop and the access VW
never becomes ready. Until that lands on its main branch, run with
`ACCESS_VW_SRC` or `ACCESS_VW_REF` pointing at a revision that has it.

`make verify` renders the e2e manifests without a cluster, so a template typo
fails in seconds rather than after kcp has been stood up.

## Toolsets

Tools are grouped into toolsets, enabled with `--toolsets` (default: `core`).
The selection is enforced server-side — a toolset that is not enabled is never
registered, so no client, model or prompt can reach it. That makes the flag
operator policy over what AI agents may do, not a client preference.

| Toolset | Default | Tools |
|---------|---------|-------|
| `core` | yes | `list_kcp_workspaces`, `list/get_kcp_apibindings`, `list/get_kcp_apiexports`, `list_kcp_apiexportendpointslices`, `list/get_kcp_workspacetypes`, `list_kcp_initializingworkspaces`, `list_kcp_terminatingworkspaces`, `list_resources`, `get_resource` |
| `apis` | no | `list/get_kcp_apiresourceschemas`, `list_kcp_apiconversions` |
| `admin` | no | `list_kcp_logicalclusters`, `list_kcp_shards`, `list_kcp_partitions`, `list_kcp_partitionsets` |
| `write` | no | `create_resource`, `update_resource`, `patch_resource`, `delete_resource`, `scale_resource` |

`core` is everything an agent needs to navigate its workspaces and read any
resource in them, including CRDs and bound APIs, via the generic
`list_resources`/`get_resource`. `apis` is API provider machinery, `admin` is
kcp operator internals (shards and topology exist only in the root workspace),
and `write` is free-form mutation — all opt-in because they are noise or risk
for ordinary callers. Every tool remains subject to the caller's scope and to
kcp's authorization through impersonation regardless of toolset selection.

## Status

Alpha. The tools live in `pkg/tools/*` subpackages, exported so other MCP
servers can reuse them by implementing `tools.Scope`. Writes act as the caller
through impersonation, so kcp's RBAC decides what is allowed.

## FAQ
**Why no local access graph.** This component could run its own RBAC indexer, but
that would double the watch load on kcp and produce two answers that can
disagree. Asking the access virtual workspace instead keeps one source of truth,
and makes `SelfClusterAccessReview` earn its keep as a real API rather than an
internal function call. The cost is an HTTP hop, which the per-caller cache
(`--access-cache-ttl`, default 30s) amortises across the many tool calls a single
MCP session makes. The access graph is already eventually consistent, so the TTL
widens an existing staleness window rather than introducing a new one.

**Why impersonation, not the caller's token.** Behind kcp's front-proxy the
caller's bearer token is consumed by the proxy, which forwards identity as
`X-Remote-*` headers instead. There is no token left to replay, so this server
acts through impersonation: kcp authorizes every per-workspace request as the
caller, and the audit log records both identities. That requires `impersonate` on
`users` and `groups` in the core group, and on `userextras/*` in
`authentication.k8s.io`.
