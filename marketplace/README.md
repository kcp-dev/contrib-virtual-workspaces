# Marketplace

A provider/offering marketplace for [kcp](https://github.com/kcp-dev/kcp):
two cluster-scoped resources — **Provider**, **Offering** — that an
operator turns into provider workspaces and per-export `bind` grants, and
a virtual workspace that serves the catalog read-only. Enabling an
offering is **kcp itself**: the consumer creates a plain `APIBinding` in
their own workspace, kcp checks the `bind` grant publishing materialized,
and the marketplace has no footprint in consumer workspaces at all.

The design, and why every piece is shaped the way it is, lives in
[DESIGN.md](DESIGN.md). This file is how to use it.

## The model

`marketplace-vw init` creates three workspaces. Everything below them is
bound by a `WorkspaceType` or by the owner's own consent — never by hand
and never by an admin; the operator holds no admin client at all.

```
root
└── marketplace
    ├── controllers ..................... what init installs, bound by nobody
    │     APIExport marketplace-catalog ....... exports Providers
    │     APIExport marketplace-publish ....... exports Offerings; claims RBAC,
    │                                           apiexports (read), logicalclusters
    │     APIExport marketplace-provisioner ... NO resources; claims workspaces
    │     WorkspaceType provider .............. no tenancy, no topology
    │
    ├── catalog ........................ the registry      (--catalog-workspace)
    │     Provider "acme" .................... the record, and only the record
    │
    └── providers ...................... provisioning parent (--providers-workspace)
          │
          └── acme ...................... WorkspaceType: provider
                APIBinding marketplace-publish  ... from the WorkspaceType
                APIExport things.acme.example ..... the provider's OWN export
                Offering "things" ................. published by the provider
                ClusterRole/CRB: bind on the export → consumers

any workspace at all (a tenancy project, a plain workspace — no type needed)
      APIBinding things.acme.example ........... created by the CONSUMER:
                                                 enabling IS this object
```

| Object | Lives in | Written by | Reached through |
|---|---|---|---|
| `Provider` | the catalog | an admin | `marketplace-catalog` |
| `Offering` | the provider's workspace | the provider | `marketplace-publish` |
| bind ClusterRole/CRB | the provider's workspace | the operator | `marketplace-publish` claims |
| the offering's `APIBinding` | the consumer's workspace | **the consumer**, natively | kcp, no marketplace involved |

Three facts carry the security model:

- **Registration is a `bind` grant.** Publishing an Offering materializes
  `bind` on that one export in the provider's workspace, to the configured
  consumers (default: any authenticated user; `--bind-user`/`--bind-group`
  restrict it). kcp checks that verb on every APIBinding — deleting the
  Offering revokes it, and being admin of one's own workspace grants
  nothing on the provider's side.
- **Consent is the consumer's own object.** The claims an offering asks
  for are accepted on the APIBinding the consumer authors, which nothing
  in the marketplace can write. A provider changing their claims bumps the
  catalog revision; existing bindings are structurally untouchable.
- **Who may enable is the workspace's RBAC, natively.** Creating an
  APIBinding needs the consumer's own rights in their own workspace —
  which is exactly what a [tenancy](../tenancy/) `admin` Membership
  materializes. The two components compose through kcp alone, with no
  knowledge of each other.

## Run it locally

Nine steps, from nothing to a user enabling an offering through the
virtual workspace with only their own certificate. Run them from the
**repository root**, each long-running one in its own shell.

If you would rather have it done for you, `make test-e2e-marketplace`
performs exactly these steps and then runs the suite against them —
[hack/ci/run-e2e-tests.sh](hack/ci/run-e2e-tests.sh) is the executable
version of this section, and it leaves a working `.e2e/` behind to poke
at.

### 1. Build

```sh
make build-marketplace      # bin/marketplace-vw
```

One binary, three subcommands (`init`, `operator`, `virtualworkspace`),
one image (`ghcr.io/kcp-dev/contrib-virtual-workspaces/marketplace-vw`).

### 2. Generate certificates

The virtual workspace authenticates callers itself, and `kcp start` mints
no client CA, so you make one. A certificate's **CommonName becomes the
username** — the string SubjectAccessReviews carry and RBAC matches.

```sh
mkdir -p pki && (cd pki

openssl req -x509 -newkey rsa:2048 -nodes -days 30 \
  -keyout ca.key -out ca.crt -subj "/CN=marketplace-dev-ca"

for spec in "alice:team-a" "bob:team-b"; do
  user="${spec%%:*}"; group="${spec#*:}"
  openssl req -newkey rsa:2048 -nodes \
    -keyout "$user.key" -out "$user.csr" -subj "/CN=$user/O=$group"
  openssl x509 -req -in "$user.csr" -CA ca.crt -CAkey ca.key \
    -CAcreateserial -days 30 -out "$user.crt"
done)
```

### 3. Run kcp

From a kcp checkout (`make build` there, or `go build ./cmd/kcp`):

```sh
kcp start --root-directory .kcp --secure-port 6443 --client-ca-file pki/ca.crt
```

`--client-ca-file` matters for step 9: an enabled API is served by *kcp*,
so a user reaching it with a certificate kcp was not told to trust arrives
as `system:anonymous`. If something already listens on 6443 — Docker
Desktop and OrbStack both do — pick another port here and match it below.

Then, in every other shell:

```sh
export KUBECONFIG=$PWD/.kcp/admin.kubeconfig
```

### 4. Install the exports

```sh
bin/marketplace-vw init --kubeconfig "$KUBECONFIG"
```

This creates the three workspaces from [the model](#the-model) and nothing
below them. Idempotent: run it twice and the second run is a no-op.

### 5. Run the operator

```sh
bin/marketplace-vw operator --kubeconfig "$KUBECONFIG" \
  --workspace-path root:marketplace:controllers \
  --catalog-workspace root:marketplace:catalog \
  --providers-workspace root:marketplace:providers
```

The per-offering `bind` grants it materializes name `system:authenticated`
by default — any authenticated user may enable, in a workspace their own
RBAC lets them bind in. `--bind-user`/`--bind-group` restrict who.

### 6. Run the virtual workspace

```sh
bin/marketplace-vw virtualworkspace --kubeconfig "$KUBECONFIG" \
  --workspace-path root:marketplace:controllers \
  --secure-port 9446 \
  --client-ca-file pki/ca.crt
```

### 7. Register a provider and publish an offering

As the administrator: a `Provider` record in the catalog makes the
operator provision `root:marketplace:providers:acme` with the `provider`
WorkspaceType, which carries the `marketplace-publish` binding — the
provider's whole surface.

```sh
kubectl --server "https://localhost:6443/clusters/root:marketplace:catalog" \
  apply -f - <<'EOF'
apiVersion: marketplace.contrib.kcp.io/v1alpha1
kind: Provider
metadata:
  name: acme
spec:
  displayName: Acme
  mode: provisioned
EOF
```

Wait for `PHASE: Ready`, then act as the provider inside that workspace.
A provider brings their own APIExport — the marketplace never defines what
a provider serves, only how it is offered:

```sh
PROVIDER="https://localhost:6443/clusters/root:marketplace:providers:acme"

kubectl --server "$PROVIDER" apply -f - <<'EOF'
apiVersion: apis.kcp.io/v1alpha1
kind: APIResourceSchema
metadata:
  name: v1alpha1.things.acme.example
spec:
  group: acme.example
  names:
    kind: Thing
    listKind: ThingList
    plural: things
    singular: thing
  scope: Cluster
  versions:
    - name: v1alpha1
      served: true
      storage: true
      schema:
        type: object
        properties:
          apiVersion: {type: string}
          kind: {type: string}
          metadata: {type: object}
          spec:
            type: object
            properties:
              message: {type: string}
---
apiVersion: apis.kcp.io/v1alpha2
kind: APIExport
metadata:
  name: things.acme.example
spec:
  resources:
    - name: things
      group: acme.example
      schema: v1alpha1.things.acme.example
      storage:
        crd: {}
  permissionClaims:
    - group: ""
      resource: configmaps
      verbs: [get, list, watch]
---
apiVersion: marketplace.contrib.kcp.io/v1alpha1
kind: Offering
metadata:
  name: things
spec:
  displayName: "Things as a service"
  description: "One Thing per workspace, batteries included."
  export: things.acme.example
  claims:
    - resource: configmaps
      verbs: [get, list, watch]
EOF

kubectl --server "$PROVIDER" get offerings
```

When the offering reports `Ready`, the operator has validated
`spec.claims` against the export (the intersection lands in
`status.appliedClaims`, at `status.revision: 1`) and materialized the
`bind` grant next to it. An offering claiming something the export never
declared goes to `Error`, not to a silently trimmed catalog entry.

### 8. Create a consumer workspace and enable

A consumer workspace is **any workspace** — no marketplace type, no
marketplace bindings. Enabling is creating the APIBinding yourself: the
reference comes from the catalog entry (workspace path + export), the
claims are the offering's `status.appliedClaims`, accepted on **your**
object — that acceptance is the consent, first-class in kcp.

```sh
kubectl --server "https://localhost:6443/clusters/root" apply -f - <<'EOF'
apiVersion: tenancy.kcp.io/v1alpha1
kind: Workspace
metadata:
  name: team-a
EOF

CONSUMER="https://localhost:6443/clusters/root:team-a"

kubectl --server "$CONSUMER" apply -f - <<'EOF'
apiVersion: apis.kcp.io/v1alpha2
kind: APIBinding
metadata:
  name: things
spec:
  reference:
    export:
      path: root:marketplace:providers:acme
      name: things.acme.example
  permissionClaims:
    - group: ""
      resource: configmaps
      verbs: [get, list, watch]
      state: Accepted
      selector:
        matchAll: true
EOF

kubectl --server "$CONSUMER" get apibindings
```

When it reports `Ready`, the provider's API serves here — kcp checked the
`bind` grant in acme's workspace on the way, the one publishing the
Offering materialized:

```sh
kubectl --server "$CONSUMER" apply -f - <<'EOF'
apiVersion: acme.example/v1alpha1
kind: Thing
metadata:
  name: first
spec:
  message: enabled through a plain kcp bind
EOF
```

Disabling is deleting the binding. If acme later changes the offering's
claims, the catalog revision bumps — and your binding keeps exactly the
claims you accepted, because they live on your object and nothing in the
marketplace can write it.

### 9. Be a user

Users don't hold the admin kubeconfig. They browse the catalog through the
virtual workspace with their own credential, and enable by binding in a
workspace their own RBAC allows — which is what a tenancy `admin`
Membership materializes. Grant alice by hand here, standing in for
tenancy:

```sh
kubectl --server "$CONSUMER" apply -f - <<'EOF'
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: team-a-admin
rules:
  - apiGroups: ["*"]
    resources: ["*"]
    verbs: ["*"]
  - nonResourceURLs: ["/"]
    verbs: ["access"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: team-a-admin-alice
subjects:
  - kind: User
    name: alice
    apiGroup: rbac.authorization.k8s.io
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: team-a-admin
EOF
```

Now alice, with nothing but her certificate. `--kubeconfig /dev/null`
matters on every one of these: with `$KUBECONFIG` exported, kubectl would
merge the admin bearer token in and it silently wins over the certificate
— you would be admin wearing an alice suit.

Browse the catalog — every entry carries the bind coordinates and the
claims:

```sh
kubectl --kubeconfig /dev/null \
  --server "https://localhost:9446/services/marketplace" \
  --client-certificate pki/alice.crt --client-key pki/alice.key \
  --insecure-skip-tls-verify \
  get offerings
```

Unbind and re-bind, as alice, natively against kcp:

```sh
kubectl --kubeconfig /dev/null --server "$CONSUMER" \
  --client-certificate pki/alice.crt --client-key pki/alice.key \
  --insecure-skip-tls-verify \
  delete apibinding things

kubectl --kubeconfig /dev/null --server "$CONSUMER" \
  --client-certificate pki/alice.crt --client-key pki/alice.key \
  --insecure-skip-tls-verify \
  apply -f - <<'EOF'
apiVersion: apis.kcp.io/v1alpha2
kind: APIBinding
metadata:
  name: things
spec:
  reference:
    export:
      path: root:marketplace:providers:acme
      name: things.acme.example
  permissionClaims:
    - group: ""
      resource: configmaps
      verbs: [get, list, watch]
      state: Accepted
      selector:
        matchAll: true
EOF

kubectl --kubeconfig /dev/null --server "$CONSUMER" \
  --client-certificate pki/alice.crt --client-key pki/alice.key \
  --insecure-skip-tls-verify \
  get apibindings
```

Run the same commands with `pki/bob.crt` and `pki/bob.key` and every call
is refused — bob holds nothing in `team-a`, and it is the workspace
refusing, not the marketplace: there is no marketplace in this path at
all. Delete alice's ClusterRoleBinding and her next call fails the same
way; under tenancy, that revocation is `tenancyctl revoke`.

### BYO: a provider without provisioning

Any workspace becomes a provider workspace by binding
`marketplace-publish` itself — the binding is the consent, and deleting it
revokes every claim it carried. Register the record with `mode: external`
and the workspace's canonical path:

```sh
kubectl --server "https://localhost:6443/clusters/root:marketplace:catalog" \
  apply -f - <<'EOF'
apiVersion: marketplace.contrib.kcp.io/v1alpha1
kind: Provider
metadata:
  name: byo
spec:
  displayName: Bring-your-own
  mode: external
  workspacePath: root:my-services
EOF
```

Then, in `root:my-services`, bind the export (accepting its claims — see
[test/e2e/scenarios_vw_test.go](test/e2e/scenarios_vw_test.go) for the
full APIBinding shape) and publish Offerings exactly as in step 7. The
operator learns the workspace's cluster from the first offering published
there, matched against the record's path — a workspace no record names
publishes into the void.

## The virtual workspace

`marketplace-vw virtualworkspace` serves, behind kcp's front-proxy or
standalone:

```
GET  /services/marketplace/apis/marketplace.contrib.kcp.io/v1alpha1/offerings
GET  /debug/directory
```

Any authenticated caller may read the catalog; it answers 503 until both
its feeds (Providers from the catalog export, Offerings from the publish
export) have synced, rather than answering partially. Each entry carries
the bind coordinates — `workspacePath`, `export` — and the claims at the
current revision, so browsing the catalog is reading both the consent
form and the APIBinding you would author. The VW writes nothing,
anywhere: enabling is between the consumer and kcp.

Authentication mirrors the other components — client certificates,
OIDC/JWT via `--authentication-config` (hand it the same file kcp gets;
[../hack/dex/](../hack/dex/) writes one), or forwarded request-header
identity behind the front-proxy.

Probing by hand:

```sh
curl -ks --cert pki/alice.crt --key pki/alice.key \
  https://localhost:9446/services/marketplace/apis/marketplace.contrib.kcp.io/v1alpha1/offerings

curl -ks --cert pki/alice.crt --key pki/alice.key \
  https://localhost:9446/debug/directory
```

`{"ready":true,"providers":N,"offerings":M}` says the directory is being
fed; `ready:false` plus 503s on the catalog means one of the two exports
has no consumers yet or the sync has not finished.

## Regenerating the schemas

The `apiresourceschema-*.yaml` assets are generated, not written: `make
generate` renders CRDs from the Go types' kubebuilder markers
(controller-gen) and converts them with kcp's own `apigen` — which gives
each schema a content-addressed name (`v<date>-<git sha>.<plural>.<group>`)
and keeps the committed name when nothing changed, because
APIResourceSchemas are immutable and a changed schema must be a new
object. The script also rewrites the `schema:` references inside this
component's APIExport manifests. After changing anything under
`sdk/apis/`, run it and commit the result; if a schema's *name* changed,
update `ExportsOrder` in [config/apiexport/bootstrap.go](config/apiexport/bootstrap.go)
to the new file name.

## Testing

From the repository root:

```sh
make test-spike-marketplace   # the keystone alone: the binder composition
                              # against a real kcp, no component involved
make test-e2e-marketplace     # full stack: real kcp + operator + VW as
                              # local processes, 11 scenarios
```

The suite asserts placement and absence, because — as with tenancy — the
absence of a binding *is* the security model: which tiers carry which
bindings, that a provider workspace serves neither tenancy.kcp.io nor
topology.kcp.io, that `bind` follows the Offering and is revoked with it
(a workspace admin cannot bind an unpublished export), that a claim
change bumps the revision while the consumer's binding keeps exactly what
they accepted, that a user with workspace RBAC binds natively with their
own credential while one without cannot, and that a BYO provider is a
first-class one.

The harness ([hack/ci/run-e2e-tests.sh](hack/ci/run-e2e-tests.sh)) builds
kcp from a sibling checkout (`KCP_DIR`, default `../../kcp`) or extracts it
from `KCP_IMAGE` in CI, runs `init` twice for idempotency, and mints a
throwaway client CA (`alice`, `bob`) for the virtual workspace scenarios.

## Limits, stated plainly

1. **There is no CLI yet.** `marketplacectl` (`catalog`, `enable` — the
   latter authoring the APIBinding for you from a catalog entry) is
   designed in [DESIGN.md](DESIGN.md) but not built; today the interfaces
   are `kubectl` against kcp and against the VW's catalog, as above.
2. **Enabling means authoring an APIBinding.** The claims must be copied
   from the catalog entry (identity hashes included) into the binding by
   whoever enables. That is deliberate — the acceptance is the consent,
   on the consumer's own object — but without the CLI it is manual.
3. **The catalog revision is advisory.** A claim change bumps it so
   consumers can notice, but nothing tracks who is on which revision:
   bindings belong to consumers, and the marketplace does not read their
   workspaces.
4. **The catalog is visible to every authenticated caller.** There is no
   per-tenant catalog scoping; a deployment wanting it would filter in
   the directory.
5. **`bind` defaults to everyone authenticated.** Publishing grants
   `bind` to `system:authenticated` unless the operator is started with
   `--bind-user`/`--bind-group`. Enabling still requires rights in a
   workspace of one's own — the grant only opens the export side.
6. **Providers police their own maximal surface.** The operator
   intersects offering claims with the export's declaration, but setting
   a `MaximalPermissionPolicy` on the export — the hard cap kcp itself
   enforces — is the provider's job and is recommended, not automated.
7. **Unbinding removes the API, not the provider's leftovers.** Objects a
   provider's controllers created through their claims are theirs to
   garbage-collect when the cluster leaves their endpoint slice.
