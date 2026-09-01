# contrib-access-virtual-workspace

Permission-aware workspace discovery for kcp. Answers "which workspaces does this
user have access to?" in a single API call — a **SelfClusterAccessReview** (SCAR) —
instead of N `SelfSubjectAccessReview`s.

The motivating consumer is MCP: an AI agent connected to a kcp installation needs a
workspace inventory scoped to the calling user, and no such endpoint exists today.
SCAR is generic though — CLIs, dashboards and other services can use it the same way.
See [kcp-dev/kcp#3839](https://github.com/kcp-dev/kcp/issues/3839).

## Why a separate module

This is its own Go module and its own binary, rather than another virtual workspace in
`cmd/virtual-workspaces`. It maintains an in-memory RBAC index built with
multicluster-runtime, and will later host an MCP protocol server — dependencies kcp
core does not otherwise carry. Keeping it out of the root module keeps those out of
kcp's dependency graph.

It builds against `kcp-dev/sdk` and `kcp-dev/virtual-workspace-framework`, and — because
the framework compiles against kcp's cluster-aware fork of Kubernetes and Go does not
propagate `replace` across module boundaries — it mirrors that fork pin in its own
`go.mod`. `hack/verify-fork-pin.sh` fails the build when the two drift.

## How it works

1. **Indexing.** A provider watches `ClusterRoleBindings` and `RoleBindings` across
   every workspace that has bound the `access.contrib.kcp.io` APIExport, translating them into
   an in-memory permission graph of subjects (users, groups, service accounts) to
   logical clusters. Indexing is opt-in by design: a workspace is only discoverable
   once it binds the APIExport.

2. **Serving.** The binary runs a virtual-workspace root apiserver with the access VW
   registered at `/services/access`. `SelfClusterAccessReview` is a create-only REST
   resource, modelled on `SelfSubjectAccessReview`: the caller POSTs an empty object
   and gets it back with `status.clusters` filled in from the graph, for the identity
   the apiserver's authentication layer resolved.

3. **Consuming.** The response is a list of `(clusterName, endpoint)` pairs.
   `cmd/scar-to-kubeconfig` turns one into a scoped kubeconfig.

The `AccessProvider` interface is the seam: the RBAC provider ships here, and
deployments using an external authorizer (webhook, OpenFGA, other ReBAC systems)
implement the same interface without changing the SCAR API surface.

## Layout

| Path | Description |
|------|-------------|
| `cmd/access-vw` | Main binary: RBAC indexer + access VW. |
| `cmd/scar-to-kubeconfig` | Calls SCAR and writes a scoped kubeconfig. |
| `sdk/apis/access/v1alpha1` | `SelfClusterAccessReview` API types. |
| `pkg/graph` | In-memory permission graph. No kcp imports. |
| `pkg/accessprovider` | The `AccessProvider` seam. |
| `pkg/rbacprovider` | Watches CRBs/RBs, translates them into graph grants. |
| `pkg/server` | Options and wiring: serving, delegated authn, VW registration. |
| `pkg/virtual/scar` | SCAR REST storage and virtual workspace definition. |
| `config/apiexport` | `access.contrib.kcp.io` APIExport, APIResourceSchema and APIExportEndpointSlice. |
| `config/deployment` | Deployment and authentication configuration. |
| `config/examples` | APIBinding for consumer workspaces to opt in. |
| `test/e2e` | Deploys this component through kcp-operator and checks that it serves. |

## Running

```sh
make build

# Create root:access:controllers and install the schema, export, bind RBAC and
# endpoint slice there.
make init

bin/access-vw \
  --kubeconfig=$KUBECONFIG \
  --apiexport-endpointslice=access.contrib.kcp.io \
  --endpoint-base=https://localhost:6443/clusters/ \
  --secure-port=9443
```

`--apiexport-endpointslice` names the `APIExportEndpointSlice` in the exports
workspace, under the export's own name (`access.contrib.kcp.io`). `init` applies
one; current kcp also creates it for the `APIExport` itself, so that apply is
usually a no-op.

Its `status.endpoints` stay empty until some workspace has an `APIBinding` to the
export — kcp only advertises a URL where there is something to serve. An
endpoint-less slice right after installation is therefore expected, and the
server engages each cluster as it appears.

Serving is TLS-only; a self-signed certificate is generated for development if none is
supplied. Callers are authenticated by the standard delegated stack — front-proxy
requestheader client certificates in production, bearer tokens via TokenReview when
reached directly.

Issue a review:

```sh
curl -k -XPOST -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" -d '{}' \
  https://localhost:9443/services/access/apis/access.contrib.kcp.io/v1alpha1/selfclusteraccessreviews
```

### Behind the front-proxy

Add to the front-proxy path-mapping file:

```yaml
- path: /services/access
  backend: https://access-vw.kcp-system.svc:9443
  backend_server_ca: /etc/kcp/tls/access-vw-ca.crt
  proxy_client_cert: /etc/kcp/tls/requestheader.crt
  proxy_client_key: /etc/kcp/tls/requestheader.key
```

and start the VW with `--requestheader-client-ca-file` pointing at the CA that signed
the front-proxy's requestheader client certificate.

## Tests

`make test` runs the unit tests. They never leave the process.

`make test-e2e` deploys this component the way a user does and checks that the result
serves:

```sh
make test-e2e
```

It creates a kind cluster, installs cert-manager and kcp-operator, builds this
checkout into an image, and then deploys a `VirtualWorkspace` object with
`access-vw-init` as an init container — the same shape as
[kcp-operator's sample][sample], which uses this component as its example of a
server that is not kcp's own. It then asserts that the operator rendered a
Deployment this image accepts, that the init container installed the APIExport and
its endpoint slice, that the init container and the server ran as different
identities, and finally that a `SelfClusterAccessReview` issued through the
front-proxy reports the workspaces the caller can reach and no others.

kcp-operator is deployed from its **main branch** by default, which is the point:
the support for custom virtual workspaces with init containers lives there, and this
is what tells us whether the two still fit together. Because it tracks a moving
branch, the e2e job can go red without anything here having changed — that is the
signal it exists to give.

```sh
# Keep the cluster and namespaces afterwards for inspection.
make test-e2e-keep

# Test against a kcp-operator release instead of main.
KCP_OPERATOR_REF=v0.1.0 ./hack/ci/run-e2e-tests.sh

# Reuse a cluster that already has cert-manager and kcp-operator.
USE_EXISTING_CLUSTER=true KUBECONFIG=... ./hack/ci/run-e2e-tests.sh
```

Needs `kind`, `kubectl`, `helm`, `docker` and `git`. See the header of
[`hack/ci/run-e2e-tests.sh`](hack/ci/run-e2e-tests.sh) for the full set of knobs.

[sample]: https://github.com/kcp-dev/kcp-operator/blob/main/config/samples/operator.kcp.io_v1alpha1_virtualworkspace_custom.yaml

## Status and known limits

Alpha. The SCAR API is `v1alpha1` and unstable.

The graph is eventually consistent: it is driven by watch events, so an access change
takes a moment to appear in SCAR results. Warrants and scopes are not yet modelled, so
in deployments that rely on them the answer can be wider than kcp's effective
authorization — the graph is derived from bindings alone. Treat SCAR as discovery, not
as an authorization decision: every subsequent call is still authorized by kcp.
