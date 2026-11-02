# Marketplace

A producer/offering marketplace for [kcp](https://github.com/kcp-dev/kcp):
**one** cluster-scoped resource — **Producer** — that an operator turns
into producer workspaces, plus per-export `bind` grants, plus a virtual
workspace that serves the catalog read-only.

An offering has no resource of its own. A producer publishes by annotating
their **APIExport**, so the offering *is* the export. And enabling one is
**kcp itself**: the consumer creates a plain `APIBinding` in their own
workspace, kcp checks the `bind` grant publishing materialized, and the
marketplace has no footprint in consumer workspaces at all.

Why each piece is shaped this way is explained where it is enforced: the
tiers in [config/apiexport/](config/apiexport/), the publish flow in
[pkg/operator/publish.go](pkg/operator/publish.go), and what an annotated
export means in [pkg/offering/](pkg/offering/). This file is how to use
it.

## The model

`marketplace-vw init` creates three workspaces. Everything below them is
bound by a `WorkspaceType` or by the owner's own consent — never by hand
and never by an admin; the operator holds no admin client at all.

```
root
└── marketplace
    ├── controllers ..................... what init installs, bound by nobody
    │     APIExport marketplace-catalog ....... exports Producers
    │     APIExport marketplace-publish ....... NO resources; claims RBAC,
    │                                           apiexports (read), logicalclusters
    │     APIExport marketplace-provisioner ... NO resources; claims workspaces
    │     WorkspaceType producer .............. no tenancy, no topology
    │
    ├── catalog ........................ the registry      (--catalog-workspace)
    │     Producer "acme" .................... the record, and only the record
    │
    └── producers ...................... provisioning parent (--producers-workspace)
          │
          └── acme ...................... WorkspaceType: producer
                APIBinding marketplace-publish  ... from the WorkspaceType
                APIExport things.acme.example ..... the producer's OWN export,
                                                    annotated: it IS the offering
                ClusterRole/CRB: bind on the export → consumers

any workspace at all (a tenancy project, a plain workspace — no type needed)
      APIBinding things.acme.example ........... created by the CONSUMER:
                                                 enabling IS this object
```

| Object | Lives in | Written by | Reached through |
|---|---|---|---|
| `Producer` | the catalog | an admin | `marketplace-catalog` |
| the offering (annotations on an `APIExport`) | the producer's workspace | the producer | `marketplace-publish` claims |
| bind ClusterRole/CRB | the producer's workspace | the operator | `marketplace-publish` claims |
| the offering's `APIBinding` | the consumer's workspace | **the consumer**, natively | kcp, no marketplace involved |

Three facts carry the security model:

- **Registration is a `bind` grant.** Publishing materializes `bind` on
  that one export in the producer's workspace, to the configured consumers
  (default: any authenticated user; `--bind-user`/`--bind-group` restrict
  it). kcp checks that verb on every APIBinding — removing the annotation
  revokes it, and being admin of one's own workspace grants nothing on the
  producer's side. A workspace no `Producer` record names gets no grant at
  all, so annotating your own export is not a way onto the catalog.
- **The offering cannot misdescribe the export.** It is the export: the
  claim surface the catalog advertises is the export's own
  `spec.permissionClaims`, read straight off it. There is no second
  declaration to validate, to intersect, or to drift.
- **Consent is the consumer's own object.** Those claims are accepted on
  the APIBinding the consumer authors, which nothing in the marketplace can
  write. A producer changing their claims changes the catalog's revision;
  existing bindings are structurally untouchable.
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

`--platform-admin-user` and `--platform-admin-group` (both repeatable) name
who may register Producers: they are bound to
`marketplace.platform.kcp.io:platform-admin` in the catalog, a role that
may enter it and manage Producers and nothing else. The binding holds
exactly the identities of the latest run, so dropping one from the flags
revokes it.

### 5. Run the operator

```sh
bin/marketplace-vw operator --kubeconfig "$KUBECONFIG" \
  --workspace-path root:marketplace:controllers \
  --catalog-workspace root:marketplace:catalog \
  --producers-workspace root:marketplace:producers
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

### 7. Register a producer and publish an offering

> Ready-made versions of everything in this step live in
> [config/examples/](config/examples/), with a script that applies them to
> the right workspaces: `marketplace/config/examples/apply.sh`, or
> `tilt trigger marketplace-examples` under Tilt. The walkthrough below is
> the same thing spelled out.

As the administrator: a `Producer` record in the catalog makes the
operator provision `root:marketplace:producers:acme` with the `producer`
WorkspaceType, which carries the `marketplace-publish` binding — the
producer's whole surface.

```sh
kubectl --server "https://localhost:6443/clusters/root:marketplace:catalog" \
  apply -f - <<'EOF'
apiVersion: marketplace.platform.kcp.io/v1alpha1
kind: Producer
metadata:
  name: acme
spec:
  displayName: Acme
  mode: provisioned
EOF
```

Wait for `PHASE: Ready`, then act as the producer inside that workspace.
A producer brings their own APIExport — the marketplace never defines what
a producer serves, only how it is offered:

```sh
PRODUCER="https://localhost:6443/clusters/root:marketplace:producers:acme"

kubectl --server "$PRODUCER" apply -f - <<'EOF'
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
  # These three annotations are the offering. There is no other object.
  annotations:
    marketplace.kcp.hub/publish: "true"
    marketplace.kcp.hub/display-name: "Things as a service"
    marketplace.kcp.hub/description: "One Thing per workspace, batteries included."
spec:
  resources:
    - name: things
      group: acme.example
      schema: v1alpha1.things.acme.example
      storage:
        crd: {}
  # And these are the consent form: what the catalog advertises and what a
  # consumer accepts are this list, not a copy of it.
  permissionClaims:
    - group: ""
      resource: configmaps
      verbs: [get, list, watch]
EOF

kubectl --server "$PRODUCER" get clusterrole marketplace:bind:things.acme.example
```

That ClusterRole appearing is the operator having seen the annotation:
publishing materializes `bind` on this one export, and nothing else. There
is no status to wait for, because there is no object to carry one — the
catalog derives everything it shows from the export itself.

Only the exact value `"true"` publishes. A typo leaves the export off the
catalog rather than on it, which is the safe direction: being on it is what
hands `bind` to strangers.

To take it off the catalog again, remove the annotation — the grant goes
with it, and the export and everyone already bound to it are untouched:

```sh
kubectl --server "$PRODUCER" annotate apiexport things.acme.example \
  marketplace.kcp.hub/publish-
```

### 8. Create a consumer workspace and enable

A consumer workspace is **any workspace** — no marketplace type, no
marketplace bindings. Enabling is creating the APIBinding yourself: the
reference comes from the catalog entry (workspace path + export), the
claims are the entry's `claims`, accepted on **your** object — that
acceptance is the consent, first-class in kcp.

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
      path: root:marketplace:producers:acme
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

When it reports `Ready`, the producer's API serves here — kcp checked the
`bind` grant in acme's workspace on the way, the one publishing
materialized:

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

Disabling is deleting the binding. If acme later changes the export's
claims, the catalog shows a different `revision` — and your binding keeps
exactly the claims you accepted, because they live on your object and
nothing in the marketplace can write it.

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
      path: root:marketplace:producers:acme
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

### BYO: a producer without provisioning

Any workspace becomes a producer workspace by binding
`marketplace-publish` itself — the binding is the consent, and deleting it
revokes every claim it carried. Register the record with `mode: external`
and the workspace's canonical path:

```sh
kubectl --server "https://localhost:6443/clusters/root:marketplace:catalog" \
  apply -f - <<'EOF'
apiVersion: marketplace.platform.kcp.io/v1alpha1
kind: Producer
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
full APIBinding shape) and annotate your APIExports exactly as in step 7.
The operator learns the workspace's cluster from the first published
export there, matched against the record's path — a workspace no record
names publishes into the void, and gets no `bind` grant for trying.

## Instructions: how a producer says "here is how to use this"

An offering can ship setup instructions — per client, per scenario — as
text a consumer sees in the console after enabling it. Two annotations on
the APIExport:

```yaml
metadata:
  annotations:
    marketplace.kcp.hub/instructions: things-instructions   # a ConfigMap, same workspace
    marketplace.kcp.hub/docs-url: https://docs.example/things
```

The ConfigMap's keys are sections, shown as tabs; a leading `NN-` orders
them and is dropped from the label, so `10-claude-code.md` reads as
*Claude Code*. The content is a small markdown subset — headings,
paragraphs, lists, fenced code, `inline code`, `[links](https://…)` — and
is rendered as elements rather than HTML, so a producer can write a code
block but not a script, and only `http(s)` links become links.

Inside the text, `{{variables}}` are filled in for whoever is reading, so a
snippet is copy-and-run rather than copy-and-edit:

| Variable | Value |
|---|---|
| `{{server}}` | the front-proxy, e.g. `https://kcp.localhost:8443` |
| `{{workspace}}` | the reader's project workspace URL |
| `{{cluster}}` | its logical cluster id |
| `{{tenant}}`, `{{project}}` | where they are working |
| `{{user}}` | who they are, as the servers see them |
| `{{export}}`, `{{producerWorkspace}}` | the offering's bind coordinates |
| `{{binding}}` | the APIBinding that enabled it |
| `{{issuer}}`, `{{clientId}}` | the OIDC issuer the console signs in with, and its OAuth client id — for instructions that point another program at the platform |

An unknown variable is left as written, so a typo shows on screen instead
of vanishing.

The ConfigMap is found by name, in any namespace of the producer's
workspace — but the namespace has to exist, and a workspace the operator
provisioned has none to start with (only `kcp-system`). The example ships
its own `Namespace` for that reason; a producer that already has one uses
it. Two ConfigMaps of the same name in different namespaces would collide
on the name, so keep it unique in the workspace.

One operational note, learned adding this: the `configmaps` claim was added
to `marketplace-publish` after producer workspaces already existed, and
**kcp does not add a new claim to a binding that already exists** — a
provisioned producer's binding keeps the claims it accepted at creation.
A fresh `init` is unaffected; an upgraded stack needs each existing publish
binding to accept the new claim (or be recreated) before that producer's
instructions become readable. A ConfigMap rather than more annotations because setup
instructions for several clients are paragraphs and code blocks, not a
line; it is read through the publish export's `configmaps` claim,
read-only, and nothing but its data is read. The mcp component is the
full example — see [mcp/config/apiexport/instructions.yaml](../mcp/config/apiexport/instructions.yaml).

## The virtual workspace

`marketplace-vw virtualworkspace` serves, behind kcp's front-proxy or
standalone:

```
GET  /services/marketplace/apis/marketplace.platform.kcp.io/v1alpha1/offerings
GET  /debug/directory
```

Any authenticated caller may read the catalog; it answers 503 until both
its feeds (Producers from the catalog export, published APIExports through
the publish export) have synced, rather than answering partially. Each
entry carries the bind coordinates — `workspacePath`, `export` — and the
claims, so browsing the catalog is reading both the consent form and the
APIBinding you would author. `revision` is a digest of those claims: the
same claims always give the same revision, so a consumer who kept theirs
can tell whether the form moved. The VW writes nothing,
anywhere: enabling is between the consumer and kcp.

Authentication mirrors the other components — client certificates,
OIDC/JWT via `--authentication-config` (hand it the same file kcp gets;
[../hack/dex/](../hack/dex/) writes one), or forwarded request-header
identity behind the front-proxy.

Probing by hand:

```sh
curl -ks --cert pki/alice.crt --key pki/alice.key \
  https://localhost:9446/services/marketplace/apis/marketplace.platform.kcp.io/v1alpha1/offerings

curl -ks --cert pki/alice.crt --key pki/alice.key \
  https://localhost:9446/debug/directory
```

`{"ready":true,"producers":N,"offerings":M}` says the directory is being
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
                              # local processes, 10 scenarios
```

The suite asserts placement and absence, because — as with tenancy — the
absence of a binding *is* the security model: which tiers carry which
bindings, that a producer workspace serves neither tenancy.kcp.io nor
topology.kcp.io, that `bind` follows the publish annotation and is revoked
when it is removed (a workspace admin cannot bind an unpublished export),
that a claim change changes the revision while the consumer's binding keeps
exactly what they accepted, that a user with workspace RBAC binds natively with their
own credential while one without cannot, and that a BYO producer is a
first-class one.

The harness ([hack/ci/run-e2e-tests.sh](hack/ci/run-e2e-tests.sh)) builds
kcp from a sibling checkout (`KCP_DIR`, default `../../kcp`) or extracts it
from `KCP_IMAGE` in CI, runs `init` twice for idempotency, and mints a
throwaway client CA (`alice`, `bob`) for the virtual workspace scenarios.

## Limits, stated plainly

1. **Enabling means authoring an APIBinding.** `contribctl offers` and
   `contribctl enable` (in [pkg/cli/](pkg/cli/)) do it for you from a
   catalog entry; by hand, the claims must be copied from that entry —
   identity hashes included — into the binding. Either way the acceptance
   is the consent, on the consumer's own object, which is the point.
2. **The catalog revision is advisory.** A claim change changes it so
   consumers can notice, but nothing tracks who is on which revision:
   bindings belong to consumers, and the marketplace does not read their
   workspaces.
3. **The catalog is visible to every authenticated caller.** There is no
   per-tenant catalog scoping; a deployment wanting it would filter in
   the directory.
4. **`bind` defaults to everyone authenticated.** Publishing grants
   `bind` to `system:authenticated` unless the operator is started with
   `--bind-user`/`--bind-group`. Enabling still requires rights in a
   workspace of one's own — the grant only opens the export side.
5. **Producers police their own maximal surface.** The catalog advertises
   whatever the export declares — it is the producer's own object, so
   there is nothing to check it against. Setting a
   `MaximalPermissionPolicy` on the export — the hard cap kcp itself
   enforces — is the producer's job and is recommended, not automated.
6. **Unbinding removes the API, not the producer's leftovers.** Objects a
   producer's controllers created through their claims are theirs to
   garbage-collect when the cluster leaves their endpoint slice.
