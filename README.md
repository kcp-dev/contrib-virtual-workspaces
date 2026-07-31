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
| `internal/mcp` | the virtual workspace: serving, authentication, per-caller scope, tools. |
| `deploy/helm` | chart. |
| `deploy/kcp` | front-proxy path mapping and the RBAC this server's identity needs. |

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

## Status

Alpha, and depends on an unreleased `SelfClusterAccessReview` API. `go.mod`
currently resolves the access virtual workspace through a local `replace`; that
becomes a version once that repository tags one.

Not yet ported from the proof of concept: the tool handlers themselves
(`internal/mcp/tools`), which supply `list_resources`, `get_resource`, the write
operations and the kcp-specific workspace tools.

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
`users`, `groups` and `userextras`.
