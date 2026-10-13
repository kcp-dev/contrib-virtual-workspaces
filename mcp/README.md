# MCP Virtual Workspace

Serves the [Model Context Protocol](https://modelcontextprotocol.io) as a kcp
virtual workspace, scoped to what the calling user can actually see.

An agent connected to a kcp installation needs two things: a list of workspaces
it may work on, and a way to act in them as the human who asked. This component
provides the second. The first comes from
the [access virtual workspace](../access/) in this repository,
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
| `cmd` | cobra commands: `serve`, `init`, `operator`. |
| `config/apiexport` | the marketplace offering: the `mcp` APIExport, its `MCPEndpoint` schema, the client instructions ConfigMap, the `bind` grant. |
| `pkg/bootstrap` | `init`: applies `config/apiexport` into `root:mcp:controllers` and mints the server's identity. |
| `pkg/operator` | watches the export's endpoint slice; writes the impersonation grant and the `MCPEndpoint` report into each workspace that enabled MCP. |
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

## Connecting a client

Any MCP client that can send a bearer token works: the front-proxy
validates it against the same issuer as every other kcp request and
forwards the identity, and the server then acts as that person. In the
Tilt stack the issuer is dex at `https://dex.kcp.localhost:8443/dex`, the
server is `https://kcp.localhost:8443/services/mcp`, and the console's own
OAuth client — `kcp`, public, PKCE — is the one to use.

### Why dynamic client registration does not work here

The MCP spec expects a client to find its authorization server by itself:
fetch RFC 9728 metadata from the resource, fetch the server's RFC 8414
metadata from the issuer, register a client there (RFC 7591), sign in.
This server implements its half — `--oauth-authorization-servers` turns on
the `/.well-known/oauth-protected-resource` endpoint and the
`WWW-Authenticate` hint on every 401, served ahead of the authentication
filters — but two things in front of and behind it stop the flow:

- **kcp's front-proxy authenticates everything.** An unauthenticated
  request for the metadata never reaches this server; the proxy answers
  it with its own 401, a Kubernetes `Status` with no `WWW-Authenticate`.
  The client then falls back to treating the server's origin as the
  authorization server and POSTs `/register` to it — which is the
  `Dynamic Client Registration rejected (HTTP 401): {"kind":"Status",…}`
  Claude Code reports.
- **dex has no registration endpoint.** Its metadata carries no
  `registration_endpoint`, so even a client that found it could not
  register.

Until that changes — the honest fix is this server on its own hostname,
authenticating OIDC tokens directly so discovery is reachable, plus a
registration endpoint that hands every registrant the public client —
clients have to be given the client up front.

### Claude Code

Pre-registered client, with discovery pointed straight at the issuer:

```sh
claude mcp add-json kcp '{"type":"http","url":"https://kcp.localhost:8443/services/mcp","oauth":{"clientId":"kcp","callbackPort":8000,"authServerMetadataUrl":"https://dex.kcp.localhost:8443/dex/.well-known/openid-configuration"}}'
claude mcp login kcp
```

No secret — `kcp` is a public client — and port 8000 is a redirect URI dex
has for it (`hack/tilt/dex.yaml`). The plain `claude mcp add` form fails
for the reasons above. Claude Code runs on Node, which does not share the
browser's certificate exceptions, and the stack uses two self-signed CAs
(kcp's and dex's), so start it from a shell with both in one bundle:

```sh
kubectl --kubeconfig .tilt/tilt-contrib.kubeconfig config view --raw \
  -o jsonpath='{.clusters[0].cluster.certificate-authority-data}' | base64 -d > /tmp/kcp-bundle.pem
cat .tilt/dex/ca.crt >> /tmp/kcp-bundle.pem
export NODE_EXTRA_CA_CERTS=/tmp/kcp-bundle.pem
```

### Codex

Codex's OAuth flow needs registration too, so give it a token instead.
`contribctl login` runs the browser flow against dex and caches the ID
token, which kcp accepts as a bearer:

```sh
bin/contribctl login --issuer https://dex.kcp.localhost:8443/dex --url https://kcp.localhost:8443
export KCP_MCP_TOKEN=$(jq -r '.[].idToken' ~/.config/tenancyctl/tokens.json | head -1)
```

```toml
# ~/.codex/config.toml
[mcp_servers.kcp]
url = "https://kcp.localhost:8443/services/mcp"
bearer_token_env_var = "KCP_MCP_TOKEN"
```

Codex trusts the system store, so import both CA certificates there.

### Checking without a client

The same thing by hand: a token from dex's password grant (development
only; `enablePasswordDB` in the Tilt dex config) and an `initialize`
through the front-proxy, the request the e2e tests also start from:

```sh
TOKEN=$(curl -sk -X POST https://dex.kcp.localhost:8443/dex/token \
  -d grant_type=password -d client_id=kcp -d scope='openid email groups' \
  -d username=alice@team-a.example.com -d password=password | jq -r .access_token)
curl -sk -X POST https://kcp.localhost:8443/services/mcp \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"probe","version":"0"}}}'
```

A 200 with `"serverInfo":{"name":"kcp"}` means the whole chain — proxy,
token, server — is fine, and anything a client still reports is on the
client's side: usually the certificate, or registration.

### The same instructions, in the console

Whoever enables MCP in a project sees these steps on the producer's page
in the console, with the server, issuer, client id and their own
workspace filled in. They come from
[`config/apiexport/instructions.yaml`](config/apiexport/instructions.yaml),
a ConfigMap the `mcp` APIExport names in its
`marketplace.platform.kcp.io/instructions` annotation; `init` applies it
with the export, and the marketplace mirrors it into the catalog. To
change what people are told, edit that file and apply it into the
controllers workspace — no rebuild:

```sh
kubectl --kubeconfig .tilt/tilt-contrib.kubeconfig \
  --server https://kcp.localhost:8443/clusters/root:mcp:controllers \
  apply -f mcp/config/apiexport/instructions.yaml
```

The format and the `{{variables}}` it may use are documented in the
[marketplace README](../marketplace/README.md#instructions-how-a-producer-says-here-is-how-to-use-this).

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

kcp-operator tracks a moving branch, so the job can go red without anything
here having changed — which is the point. Pin it with `KCP_OPERATOR_REF`. The
access virtual workspace is built from this repository at the same revision,
so the two virtual workspaces can never drift apart. The script's header
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
`list_resources`/`get_resource`. `apis` is API producer machinery, `admin` is
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

**Enabling MCP is a marketplace offering.** MCP is not an API, so there is
nothing for a consumer to bind in order to "get" it. What they actually
have to do is let this server act as them in their workspace — see the
impersonation note below — and that is a per-workspace, opt-in, revocable
grant, which is exactly the shape of an enablement.

So `config/apiexport/` publishes one APIExport whose permission claims
cover the RBAC that grant needs. Binding it is the consent; `mcp operator`
watching the export's endpoint slice spends those claims on the single
impersonation ClusterRole and ClusterRoleBinding, and then writes an
`MCPEndpoint` into the workspace so whoever enabled it can see that it
worked and where to point a client. Deleting the binding revokes the
claims and with them the operator's reach — consent and capability are the
same fact, rather than two things that can drift.

`MCPEndpoint` has no spec on purpose: there is nothing to ask for, because
the binding is the request. It is a report.

**Why impersonation, not the caller's token.** Behind kcp's front-proxy the
caller's bearer token is consumed by the proxy, which forwards identity as
`X-Remote-*` headers instead. There is no token left to replay, so this server
acts through impersonation: kcp authorizes every per-workspace request as the
caller, and the audit log records both identities. That requires `impersonate` on
`users` and `groups` in the core group, and on `userextras/*` in
`authentication.k8s.io`.
