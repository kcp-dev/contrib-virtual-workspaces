# Tenancy

A light tenancy model for [kcp](https://github.com/kcp-dev/kcp): three
cluster-scoped resources — **Tenant**, **Project**, **Membership** — that an
operator turns into real kcp workspaces and RBAC, and one virtual resource —
**SelfTenancyReview** — that answers "which tenants and projects am I in, and
as what role?" in a single call.

## The model

`tenancy-vw init` creates three workspaces: one holding the APIExports, one
holding the Tenant records, and one that every tenant workspace is created
under. The records and the workspace tree are deliberately different tiers,
and both paths are configurable.

Everything below them is provisioned by the operator, and — this is the
part worth internalising — **nothing below those tiers is bound by hand**.
A workspace's `WorkspaceType` carries its APIBindings, so what a tier can
do is decided when it is created.

```
root
└── tenancy
    ├── controllers ....................... what init installs, bound by nobody
    │     APIExport tenancy-platform ....... exports Tenants
    │     APIExport tenancy ................ exports Projects, Memberships
    │     APIExport tenancy-provisioner .... NO resources; claims workspaces
    │     APIExport tenancy-access ......... NO resources; claims namespaces + RBAC
    │     APIExportEndpointSlice × 4 ....... one virtual workspace each
    │     WorkspaceType tenant, project .... carry the bindings below
    │
    ├── store ............................. the registry    (--store-workspace)
    │     APIBinding tenancy-platform ...... bound by init, by hand
    │     ── and NOT tenancy-provisioner ──   no workspace is ever made here
    │     Tenant "acme" .................... the record, and only the record
    │     Tenant "globex"
    │
    └── tenants ........................... provisioning parent (--tenants-workspace)
          APIBinding tenancy-provisioner ... bound by init, by hand
          │
          ├── acme-corp ................... WorkspaceType: tenant
          │     APIBinding tenancy ......... from the WorkspaceType
          │     APIBinding tenancy-provisioner
          │     Project "web" .............. written here, not in the store
          │     Project "api"
          │     Membership "alice-admin"
          │     │
          │     ├── web ................... WorkspaceType: project
          │     │     APIBinding tenancy-access ... from the WorkspaceType
          │     │     ClusterRole  tenancy.contrib.kcp.io:role:admin
          │     │     ClusterRoleBinding ...:membership:alice-admin
          │     │     Namespace "default" ... created by the operator
          │     │
          │     └── api ................... same shape; the grant lands here too
          │
          └── globex-inc .................. another tenant, same shape
```

Splitting the registry from the tree is the same instinct as splitting the
two capability exports: the tier that can create a workspace cannot rewrite
the records that say which workspaces should exist, and the tier holding
the records cannot create one. A `Tenant` in the store names no workspace
path — the operator resolves the provisioning parent once, from its own
configuration.

Which means:

| Object | Lives in | Written by | Reached through |
|---|---|---|---|
| `Tenant` | the store | an admin / `tenancyctl` | `tenancy-platform` |
| `Project`, `Membership` | that tenant's own workspace | an admin / `tenancyctl` | `tenancy` |
| tenant `Workspace` | the tenants workspace | the operator | `tenancy-provisioner` |
| project `Workspace` | the tenant's workspace | the operator | `tenancy-provisioner` |
| `ClusterRole`, `ClusterRoleBinding`, `Namespace` | project workspaces only | the operator | `tenancy-access` |

Two of the exports declare no resources at all. That is not an oversight:
an export with no schemas is the "grant a capability, add no API" primitive.
Binding `tenancy-access` into a project gives a tenant nothing new in
`kubectl api-resources`, while making the operator's reach into that
workspace declared, scoped and auditable — `kubectl get apibindings` there
lists exactly what the platform may touch. The alternative is writing into
child workspaces with an admin client, where the same bug's blast radius is
"everything".

Provisioning a workspace and reaching inside one are kept in **separate**
exports, so they are granted and revoked separately, and neither is
ambient.

### Why a Membership never lands in the tenant workspace

A `Membership` grants one subject (`User` or `Group`, matched verbatim
against the identity kcp authenticates) one role — `view`, `edit` or
`admin` — and the operator materializes it as a ClusterRole plus a
ClusterRoleBinding. Every role also carries kcp's workspace-content
`access` verb, since resource permissions are useless in a workspace the
subject cannot enter. Deleting the Membership removes the binding again; a
finalizer makes sure of it.

Those bindings appear **only in project workspaces**. A tenant-wide grant
fans out across every project of the tenant rather than landing once at the
tenant tier, and a project-scoped grant lands in exactly one.

The tenant workspace holds the Projects and Memberships that decide who can
reach what. A tenant who could write there could grant themselves anything,
and would be doing it behind the virtual workspace rather than through it —
so every guard the VW applies would be bypassed by construction rather than
merely unimplemented. Instead, no binding in that logical cluster names any
tenant identity at all. That is a stronger guarantee than a check: it
survives a new client, a forgotten code path and a misconfigured route,
because it is not a check.

The `project` type also omits `extend: root:universal` on purpose, which
keeps `tenancy.kcp.io` and `topology.kcp.io` out of a tenant's API surface
— no spawning arbitrary workspaces, no inspecting cluster topology. The
cost is that kcp no longer creates the `default` namespace, so the operator
does, which is what the namespaces claim on `tenancy-access` is for.

Further stores, if you want more than one registry, are made with
`tenancyctl org bootstrap`. The store and the provisioning parent are the
only tiers bound by hand, because nothing provisions them.

## Run it locally

Eight steps, from nothing to a user asking what they can reach. Run them
from the **repository root**, each long-running one in its own shell.

If you would rather have it done for you, `make test-e2e-tenancy` performs
exactly these steps and then runs the suite against them —
[hack/ci/run-e2e-tests.sh](hack/ci/run-e2e-tests.sh) is the executable
version of this section, and it leaves a working `.e2e/` behind to poke at.

### 1. Build

```sh
make build-tenancy      # bin/tenancy-vw and bin/tenancyctl
```

One binary, three subcommands (`init`, `operator`, `virtualworkspace`), one
image (`ghcr.io/kcp-dev/contrib-virtual-workspaces/tenancy-vw`): a
deployment's init container and both long-running processes share the
artifact. `tenancyctl` is the separate client.

### 2. Generate certificates

The virtual workspace authenticates callers itself, and `kcp start` mints
no client CA, so you make one. A certificate's **CommonName becomes the
username** and its **Organizations become the groups** — exactly the
strings a `Membership` is matched against, so `alice` here is `alice` in a
grant.

```sh
mkdir -p pki && (cd pki

openssl req -x509 -newkey rsa:2048 -nodes -days 30 \
  -keyout ca.key -out ca.crt -subj "/CN=tenancy-dev-ca"

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

`--client-ca-file` is why step 2 comes first. The virtual workspace
authenticates callers itself, so `whoami` works without it — but the
kubeconfig step 8 writes points at **kcp**, and a certificate kcp was not
told to trust makes the caller `system:anonymous` there. Leave the flag
out and step 8's `kubectl` is refused, with the same opaque message an
ungranted user gets, because kcp will not say which of the two it was.

If something already listens on 6443 — Docker Desktop and OrbStack both
do — pick another port here and use the same one in step 6's
`--endpoint-base`.

Then, in every other shell:

```sh
export KUBECONFIG=$PWD/.kcp/admin.kubeconfig
```

### 4. Install the exports

```sh
bin/tenancy-vw init --kubeconfig "$KUBECONFIG"
```

This creates the three workspaces from [the model](#the-model) —
`root:tenancy:controllers` (four APIExports, four endpoint slices, both
WorkspaceTypes), `root:tenancy:store` (bound to `tenancy-platform`) and
`root:tenancy:tenants` (bound to `tenancy-provisioner`) — and nothing
below them, because a `WorkspaceType` carries the rest. It is idempotent:
run it twice and the second run is a no-op. Quote `"$KUBECONFIG"` — unset
and unquoted, the shell drops the argument and `--kubeconfig` swallows the
next flag, giving the baffling `load kubeconfig: stat --workspace-path`.

### 5. Run the operator

```sh
bin/tenancy-vw operator --kubeconfig "$KUBECONFIG" \
  --workspace-path root:tenancy:controllers \
  --tenants-workspace root:tenancy:tenants
```

It holds no admin client: every write goes through one of the four
exports.

### 6. Run the virtual workspace

```sh
bin/tenancy-vw virtualworkspace --kubeconfig "$KUBECONFIG" \
  --workspace-path root:tenancy:controllers \
  --secure-port 9445 \
  --client-ca-file pki/ca.crt \
  --endpoint-base "https://localhost:6443/clusters/"
```

`--endpoint-base` is what the answers point users at, so it is kcp's
address as *they* reach it. `--requestheader-client-ca-file` is only for
running behind kcp's front-proxy; skip it locally.

### 7. Create a tenant, a project and a grant

As the administrator, with the kcp kubeconfig:

```sh
bin/tenancyctl tenant create acme --display-name "Acme Corp"
bin/tenancyctl project create web --tenant acme --display-name "Web Shop"
bin/tenancyctl grant alice --role admin --tenant acme
bin/tenancyctl tenant list
```

Watch the operator log: it creates `acme-corp` under `root:tenancy:tenants`,
`web` under that, and the ClusterRole and ClusterRoleBinding for alice
inside `web` — never in the tenant workspace.

### 8. Be a user

```sh
export TENANCY_VW_URL=https://localhost:9445

bin/tenancyctl whoami \
  --client-cert pki/alice.crt --client-key pki/alice.key \
  --insecure-skip-tls-verify

bin/tenancyctl kubeconfig -o acme.kubeconfig \
  --client-cert pki/alice.crt --client-key pki/alice.key \
  --insecure-skip-tls-verify
kubectl --kubeconfig acme.kubeconfig --context acme-web get namespaces
```

`--insecure-skip-tls-verify` is for the virtual workspace's self-signed
serving certificate; in a deployment pass `--certificate-authority`.

If that last `kubectl` answers `Error from server (Forbidden): unknown`,
wait a second and repeat it. `whoami` reads the Membership — the
*intent* — while `kubectl` needs the ClusterRoleBinding the operator
materializes in the project workspace a beat later, so immediately after a
grant the review is ahead of the RBAC. The error is opaque on purpose:
kcp will not tell a caller without `access` whether a workspace exists.

### With Dex instead of certificates

To exercise the real identity path — a JWT and `tenancyctl login` — start
the shared provider and hand kcp and the virtual workspace the *same*
configuration, because a username that differs between them is a grant
nobody holds:

```sh
hack/dex/dex.sh up            # writes .dex/authentication-config.yaml + CA
eval "$(hack/dex/dex.sh env)" # DEX_ISSUER, DEX_CA_FILE, DEX_PASSWORD, ...
```

Add `--authentication-config "$DEX_AUTHENTICATION_CONFIG"` to **both**
step 3's `kcp start` and step 6's `virtualworkspace`, restart them, then
log in as a real user (`alice@team-a.example.com` maps to username `alice`
in group `team-a`, the same identity the certificate carries):

```sh
printf %s "$DEX_PASSWORD" | bin/tenancyctl login \
  --issuer "$DEX_ISSUER" --certificate-authority "$DEX_CA_FILE" \
  --username alice@team-a.example.com --password-stdin

bin/tenancyctl whoami --insecure-skip-tls-verify   # no credential flags
```

`hack/dex/dex.sh down` stops it. See [../hack/dex/](../hack/dex/) for what
the claim mapping does.

## Naming

Workspace names are DNS labels; display names are arbitrary UTF-8. The
`--naming-strategy` flag picks the mapping:

- `slug` (default): lowercase the display name, collapse everything else to
  dashes ("Acme Corp" → `acme-corp`). On collision with a workspace owned by
  a different object, fall back to the slug plus a hash of the object's UID.
  Ownership is tracked with a `tenancy.contrib.kcp.io/owner-uid` label, so
  reconciles converge without storing decisions anywhere.
- `uid`: always name by UID hash. Opaque, but immune to display-name games.

Both strategies are pure functions of (display name, UID): the same object
always proposes the same candidates.

## The virtual workspace

`tenancy-vw virtualworkspace` serves one create-only resource behind kcp's
front-proxy:

```
POST /services/tenancy/apis/tenancy.contrib.kcp.io/v1alpha1/selftenancyreviews
```

The caller POSTs an empty object; the answer lists their tenants with
display names, workspace clusters, front-proxy endpoints
(`--endpoint-base` + cluster), roles, and reachable projects. The answer
comes from an in-memory directory fed by two exports at once — Tenants from
`tenancy-platform`, Projects and Memberships from `tenancy` — joined on the
tenant's workspace cluster, and is refused with 503 until both syncs
complete rather than answered partially.

Authentication mirrors the access VW: OIDC/JWT, forwarded request-header
identity, or client certificates; the configuration must match kcp's,
because subjects are compared verbatim. In practice that means handing the
same `--authentication-config` to kcp and to this server — see
[../hack/dex/](../hack/dex/), which runs an identity provider and writes
exactly that file.

## tenancyctl

The reference for the client [Run it locally](#run-it-locally) uses in
steps 7 and 8. Administrators point it at an organization workspace
(default `root:tenancy:store`); users point it at the virtual workspace
with nothing but their own credential.

```sh
# administrator, with a kcp kubeconfig
export KUBECONFIG=.kcp/admin.kubeconfig

tenancyctl tenant create acme --display-name "Acme Corp"
tenancyctl project create web --tenant acme --display-name "Web Shop"
tenancyctl grant alice --role admin --tenant acme
tenancyctl grant team-a --group --role view --tenant acme --project web

tenancyctl tenant list
tenancyctl revoke --list
tenancyctl revoke alice --tenant acme

# another platform workspace, bound for you
tenancyctl org bootstrap root:acme
tenancyctl tenant create acme --workspace root:acme
```

`grant` names the Membership deterministically from (subject, tenant,
project), so granting twice is a no-op and granting a different role is an
in-place change rather than a second object.

```sh
# user: log in once, then no credential flags at all
export TENANCY_VW_URL=https://localhost:9445   # or pass --url

tenancyctl login --issuer https://dex.example.com
tenancyctl whoami
tenancyctl kubeconfig -o acme.kubeconfig
```

`login` opens a browser, listens on a loopback address for the redirect and
exchanges the code with PKCE — no client secret, because a CLI cannot keep
one. The resulting **ID token is the JWT** every other command presents,
cached at `~/.config/tenancyctl/tokens.json` with mode `0600`. It is the
same token kcp validates, so the username it maps to is the string a
`Membership` has to name.

For scripts and tests, where there is no browser, exchange a password
instead:

```sh
printf %s "$PASSWORD" | tenancyctl login \
  --issuer "$DEX_ISSUER" --username alice@team-a.example.com --password-stdin
```

`tenancyctl logout [--all]` forgets the cached token. Credentials given
explicitly still win, which is how the certificate path keeps working:

```sh
# or bring your own credential, without logging in

tenancyctl whoami \
  --client-cert pki/alice.crt --client-key pki/alice.key --insecure-skip-tls-verify

tenancyctl kubeconfig -o acme.kubeconfig \
  --client-cert pki/alice.crt --client-key pki/alice.key --insecure-skip-tls-verify
kubectl --kubeconfig acme.kubeconfig --context acme get ...
```

The address is the one thing that cannot be guessed: behind kcp's
front-proxy the virtual workspace answers on the same host as kcp (pass
`--kubeconfig` and it is taken from there), while a locally run one is on
whatever `--secure-port` it was started with. `--insecure-skip-tls-verify`
is for a self-signed local serving certificate; in a deployment pass
`--certificate-authority` instead.

`kubeconfig` is the other half of a login: it asks the virtual workspace
what the caller can reach and writes one context per tenant and per
project, each pointing at that workspace's own URL. There is still no user
registry to enrol into — `login` obtains a credential, it does not create
an identity. Issuing identities belongs to your provider, and a
`Membership` only records what one may do.

## Where the CA files come from

Nowhere, by default — **you** create them, and `kcp start` does not.

A default `kcp start` leaves only these in `.kcp/`: `apiserver.crt` and
`apiserver.key` (kcp's *serving* cert), `sa.key` (the service-account
signing key), `etcd-server/`, and `admin.kubeconfig` — whose
`certificate-authority-data` is the CA that signs kcp's **serving** cert
(what clients use to verify the server, not to prove who they are) and
whose admin credential is a **bearer token**, not a certificate. There is
no client CA and no front-proxy CA on disk, because kcp did not need to
mint either.

So the two flags name CAs that are yours to choose:

- `--client-ca-file` — the CA whose certificates you accept from direct
  callers. The certificate's **CommonName becomes the username** and its
  **Organizations become the groups**, and those are exactly the strings a
  `Membership.spec.subject` is matched against.
- `--requestheader-client-ca-file` — the CA that signs kcp's front-proxy
  client certificate. Only needed when the VW runs *behind* the front-proxy,
  which terminates the caller's connection and forwards identity in
  `X-Remote-*` headers. For a local test, skip it.

Client certificates are a complete authentication method on their own, so
`--client-ca-file` alone is enough to start. (If you do pass the same file
to both flags, the server then also demands
`--requestheader-allowed-names`, so that an ordinary client certificate
cannot forge identity headers for an arbitrary user.)

### Where to get certificates without generating them

[Step 2](#2-generate-certificates) mints them. The e2e harness does the
same in [hack/ci/run-e2e-tests.sh](hack/ci/run-e2e-tests.sh) and leaves the
result in `tenancy/.e2e/pki`, so `make test-e2e-tenancy` once is a
shortcut to a ready-made `alice` (`team-a`), `bob` (`team-b`) and
`mallory`.

Whether kcp accepts those certificates is a separate decision from whether
the virtual workspace does: they are different servers with different
flags. The review endpoint authenticates callers itself and never
impersonates them into kcp, so it needs nothing from kcp — but a user who
will `kubectl` into their own workspace needs kcp started with the same CA,
which is what [step 3](#3-run-kcp) does.

### Probing it with curl

With the virtual workspace from step 6 running:

```sh
PKI=$PWD/pki   # or tenancy/.e2e/pki

# ask who you are:
curl -ks --cert "$PKI/alice.crt" --key "$PKI/alice.key" \
  -X POST -H 'Content-Type: application/json' \
  -d '{"apiVersion":"tenancy.contrib.kcp.io/v1alpha1","kind":"SelfTenancyReview"}' \
  https://localhost:9445/services/tenancy/apis/tenancy.contrib.kcp.io/v1alpha1/selftenancyreviews

# ...and whether the directory is being fed at all:
curl -ks --cert "$PKI/alice.crt" --key "$PKI/alice.key" \
  https://localhost:9445/debug/directory
```

Reading the answers:

- An empty `status` alongside `{"tenants":0,...}` means the directory found
  no tenant you belong to. Either nothing has been created yet, or your
  identity holds no Membership. The answer is empty rather than wrong.
- `401` means the certificate was not signed by `--client-ca-file`, or has
  expired. Check with
  `openssl verify -CAfile "$PKI/ca.crt" "$PKI/alice.crt"`.
- `403 ... authentication required` means no certificate was presented at
  all; the review is only served to an authenticated caller.

## Testing

From the repository root:

```sh
make test               # unit tests (naming, directory, RBAC shapes, storage)
make test-e2e-tenancy   # full stack: real kcp + operator + VW as local processes
```

The suite covers the CLI too: that `init` really binds
`root:tenancy:tenants`, that `tenancyctl` can drive a tenant, project and
grant end to end, and that `whoami`/`kubeconfig` answer for a user holding
only a certificate.

By default the harness also stands up Dex ([../hack/dex/](../hack/dex/))
and starts both kcp and the virtual workspace with the same
`AuthenticationConfiguration`, so four scenarios prove the identity path
end to end: that a Dex token and a client certificate get the same review,
that groups derived by claim mapping are what a `Membership` matches, that
a token reaches the project workspace the grant created RBAC in while an
ungranted token does not, and that `tenancyctl login` caches a token
`whoami` then works with and `logout` really revokes. `DEX=false` skips
them and runs on certificates alone.

The e2e harness ([hack/ci/run-e2e-tests.sh](hack/ci/run-e2e-tests.sh))
builds kcp from a sibling checkout (`KCP_DIR`, default `../../kcp`) or
extracts it from `KCP_IMAGE` in CI, runs `init` twice so idempotency is
exercised on every run, and mints a throwaway client CA whose certificates
(`alice` in `team-a`, `bob` in `team-b`, `mallory`) are what the
SelfTenancyReview scenarios authenticate with.

## Limits, stated plainly

1. **A login lasts as long as the token does.** `tenancyctl login` caches
   the ID token and nothing else — no refresh token, because nothing here
   spends one and an unused credential on disk is a liability. When the
   token expires the commands stop finding a credential and say so, and
   you log in again.
2. **Roles are fixed.** `view`, `edit`, `admin` — no custom role references.
   That keeps a Membership reviewable at a glance; a deployment wanting more
   defines it outside this component.
3. **Workspace readiness is polled** (3s), not watched: readiness lives on
   Workspace objects in claimed clusters, and a poll is simpler than
   engaging every provisioned cluster for one phase field.
4. **`SelfTenancyReview` trusts its directory**, which follows informers; a
   just-created Membership can take a moment to appear in answers, so
   `tenancyctl whoami` can lag a `grant` by a second or two.
5. **There is no user registry.** `tenancyctl` has no "create user": an
   identity exists because your IdP issued it a credential, and a
   `Membership` names it verbatim. A typo in a subject name is not an
   error, it is a grant nobody holds.
6. **There is no tenant self-service.** The virtual workspace serves
   `SelfTenancyReview` and nothing else, so Projects and Memberships are
   written straight to kcp by someone holding an admin kubeconfig —
   `tenancyctl` included. A tenant cannot manage their own projects,
   because the tier those objects live in names no tenant identity. The
   intended shape is for those writes to arrive through the virtual
   workspace, which would hold the only credential into that tier and
   could enforce guards a direct write bypasses.
7. **The operator is not an administrator, and that is load-bearing.** It
   holds no admin client; every write goes through one of the four exports,
   so its reach is exactly their claims. The flip side is that a workspace
   provisioned with the wrong `WorkspaceType` is one the operator cannot
   touch at all, because nothing bound it.
