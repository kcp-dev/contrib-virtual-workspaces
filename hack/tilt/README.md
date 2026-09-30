# The all-in-one development stack

One command brings up kcp, an OIDC issuer, and every component in this
repo, wired together: `make tilt-up`.

What you get, and what this document walks through:

| | |
|---|---|
| **kcp** | operator-deployed, two shards (`root`, `theseus`) behind a front-proxy at `https://kcp.localhost:8443` |
| **Dex** | in-cluster OIDC issuer at `https://dex.kcp.localhost:8443/dex`, trusted by kcp *and* both virtual workspaces |
| **tenancy** | virtual workspace at `/services/tenancy`, a local operator, seeded with a demo tenant and project |
| **marketplace** | virtual workspace at `/services/marketplace`, a local operator |
| **observability** | Grafana, Prometheus, Loki |

The point of the setup is what the later sections show: an ordinary user,
holding nothing but a password, ends up with a working kubeconfig for
exactly the workspaces they were granted — and can enable a marketplace
offering in one of them, with no admin credential anywhere in their path.

Every command below was executed against this stack.

---

## 1. Bring the stack up

You need a kcp checkout next to this one (`../kcp`, or set `KCP_DIR`) and
Docker. Add the hostnames the stack serves on to `/etc/hosts`:

```
127.0.0.1 kcp.localhost root.kcp.localhost theseus.kcp.localhost dex.kcp.localhost
```

Then:

```sh
make tilt-up            # creates the kind cluster and runs tilt up
```

Tilt's UI is at http://localhost:10350. Resources come up in dependency
order: kcp (operator, shards, front-proxy) → `kcp-admin extract` → `dex` →
per component `build` → `init` → `operator` + `vw` → `tenancy-seed`.

Knobs, all optional:

| Variable | Effect |
|---|---|
| `KCP_DIR` | where the kcp checkout lives (default `../kcp`) |
| `KCP_STATIC=false` | build kcp from that checkout instead of running published images |
| `TENANCY_ENABLED=false`, `MARKETPLACE_ENABLED=false` | skip a component |
| `DEX_ENABLED=false` | skip the identity provider |
| `SEED_ENABLED=false` | skip the demo tenancy data |


### Check it

Everything answers on the front-proxy, at `https://kcp.localhost:8443` —
kcp's own API under `/clusters/`, each virtual workspace under the path it
owns. There is no second address to know:

```sh
KC=.tilt/tilt-frontproxy.kubeconfig

kubectl --kubeconfig $KC get --raw \
  /services/tenancy/apis/tenancy.platform.kcp.io/v1alpha1
# {"kind":"APIResourceList",...,"resources":[{"name":"selftenancyreviews",...}]}

kubectl --kubeconfig $KC get --raw \
  /services/marketplace/apis/marketplace.platform.kcp.io/v1alpha1/offerings
# {"kind":"CatalogOfferingList",...,"items":[]}
```

The virtual workspaces run **in the cluster**, deployed by the kcp-operator
from a `VirtualWorkspace` object: it gives each one a Deployment and a
Service on `:6443`, issues its serving certificate from the shard CA, and
runs the component's own `init` as an init container so the APIExports
exist before the server that serves them starts. The front-proxy reaches
them through `additionalPathMappings` and forwards the caller's identity
in `X-Remote-*` headers over its own client certificate — which is why a
token works the same at kcp and at a virtual workspace.

The operators stay local processes (`tilt trigger <c>-operator` to
restart): they serve no path, so nothing about them needs the routing,
and that is where the reconcile loop you iterate on lives.

The tenancy numbers come from the seed (`contribctl tenancy seed`, run by
the `tenancy-seed` resource): tenant `acme` with project `web`, alice
admin, bob view. Re-run it any time with `tilt trigger tenancy-seed` — it
is idempotent.

The marketplace starts empty — [step 5](#5-the-marketplace-publish-an-api-enable-it-as-a-user)
fills it.

---

## 2. Log in with Dex

Dex is the one identity provider kcp and both virtual workspaces trust, so
a token names the same user everywhere. Three users, all with the password
`password`:

| Login | Identity everywhere |
|---|---|
| `alice@team-a.example.com` | `alice@team-a.example.com` |
| `bob@team-b.example.com` | `bob@team-b.example.com` |
| `mallory@strangers.example.com` | `mallory@strangers.example.com` |

The identity is the **plain email, with no groups**: the kcp-operator only
speaks the legacy `--oidc-*` claim mapping, so the CEL claim mapping used
by [hack/dex](../dex/) (which would produce `alice` in group `team-a`) is
not available here. Grants under Tilt therefore name the full email — and
the login form wants that email too, not the `username` in Dex's config,
which is only a display name.

Dex keeps its signing keys in the cluster (`storage: type: kubernetes`),
not in memory, so restarting it is a non-event. With memory storage it
mints new keys on every start, which invalidates every issued token *and*
leaves the front-proxy, both shards and both virtual workspaces holding a
JWKS that verifies nothing — every login then fails with `401` until each
of them is restarted too.

```sh
echo password | bin/contribctl tenancy login \
  --issuer https://dex.kcp.localhost:8443/dex \
  --certificate-authority .tilt/dex/ca.crt \
  --username alice@team-a.example.com --password-stdin
```
```
logged in to https://dex.kcp.localhost:8443/dex as alice@team-a.example.com
token cached in ~/.config/tenancyctl/tokens.json
expires Sat, 26 Sep 2026 13:01:17 EEST
```

Dropping `--username --password-stdin` opens a browser instead
(authorization code + PKCE on `localhost:8000`); the password grant is
here because scripts have no browser.

The ID token is the credential, and it is cached — nothing below passes a
credential flag again.

---

## 3. Find your tenants and projects

A new user knows nothing: not which workspaces exist, not their paths, not
even whether they have any. `whoami` asks the tenancy virtual workspace,
which authenticates the token itself and answers with the Memberships that
name the caller:

```sh
bin/contribctl tenancy whoami \
  --url https://kcp.localhost:8443/services/tenancy --insecure-skip-tls-verify
```
```
TENANT   PROJECT   DISPLAY NAME   ROLES   ENDPOINT
acme     -         Acme Corp      admin   https://kcp.localhost:8443/clusters/2p56nya1pwo7fk5z
acme     web       web            admin   https://kcp.localhost:8443/clusters/fasvvoj7giral9qt
```

No kubeconfig for kcp was involved, and no admin credential: the only
input was the cached token. Alice is admin of the tenant, and a
tenant-wide grant reaches every project, hence `admin` on `web` too.

**How does it know which user this is?** No flag says so, and the request
body carries no username. Two halves:

- the CLI passes no credential, so it falls back to what `login` cached —
  the entry for `--issuer`, or the single unexpired token when only one
  issuer is cached (with two it refuses and asks which, rather than
  guessing);
- the virtual workspace validates that JWT itself. It runs with the same
  issuer, client id and `--oidc-username-claim email` as kcp, so the
  username it derives is `alice@team-a.example.com`, and the review
  answers from the *authenticated* identity on the request — never from
  anything the caller asserts.

Which is why the mapping in [step 2](#2-log-in-with-dex) matters: the
username the virtual workspace derives has to be the exact string a
Membership names, so under Tilt grants use the full email.

The same command as bob (`--token-cache /tmp/bob-tokens.json` after
logging in as him) shows the narrower grant:

```
TENANT   PROJECT   DISPLAY NAME   ROLES   ENDPOINT
acme     -         Acme Corp      -       https://kcp.localhost:8443/clusters/2p56nya1pwo7fk5z
acme     web       web            view    https://kcp.localhost:8443/clusters/fasvvoj7giral9qt
```

Bob has no role at the tenant level and `view` on the project — the
listing shows a tenant he can see through, not one he can use.

`--insecure-skip-tls-verify` is here only because the virtual workspace
serves a development certificate; pass
`--certificate-authority .tilt/pki/ca.crt` instead to verify it properly.

---

## 4. Use the kubeconfig

`kubeconfig` turns that same answer into contexts — one per **project**
the caller can reach:

```sh
bin/contribctl tenancy kubeconfig \
  --url https://kcp.localhost:8443/services/tenancy --insecure-skip-tls-verify \
  -o /tmp/alice.kubeconfig
# wrote /tmp/alice.kubeconfig with 1 project context (current: acme-web)

kubectl --kubeconfig /tmp/alice.kubeconfig config get-contexts
```
```
CURRENT   NAME       CLUSTER    AUTHINFO   NAMESPACE
*         acme-web   acme-web   tenancy
```

Projects only, and no context for the tenant — a tenant workspace is the
record store the tenancy system keeps Tenants, Projects and Memberships
in. A Membership materializes RBAC in that tenant's *projects* and never
in the tenant itself, so no member can enter it however senior: alice's
tenant-wide `admin` means admin in every project. A context for it could
only ever answer `Forbidden`, so it is not written.

These contexts point at **kcp itself**, not at the virtual workspace, and
they carry alice's own token. kcp authenticates her against Dex and
authorizes her against the RBAC the tenancy operator materialized from her
Membership:

```sh
kubectl --kubeconfig /tmp/alice.kubeconfig --context acme-web auth whoami
```
```
ATTRIBUTE   VALUE
Username    alice@team-a.example.com
Groups      [system:authenticated]
```

Note the groups: nothing privileged. Everything she can do comes from her
Membership, and she can work in her project:

```sh
kubectl --kubeconfig /tmp/alice.kubeconfig --context acme-web get namespaces
# NAME      STATUS   AGE
# default   Active   2m31s

kubectl --kubeconfig /tmp/alice.kubeconfig --context acme-web \
  create configmap hello --from-literal=who=alice
# configmap/hello created
```

Bob's kubeconfig, built exactly the same way, is `view` on that project —
he reads alice's object and is refused a write, by kcp, on his own
identity:

```sh
kubectl --kubeconfig /tmp/bob.kubeconfig --context acme-web get configmap hello
# NAME    DATA   AGE
# hello   1      6s

kubectl --kubeconfig /tmp/bob.kubeconfig --context acme-web \
  create configmap nope --from-literal=x=y
# error: failed to create configmap: configmaps is forbidden:
#   User "bob@team-b.example.com" cannot create resource "configmaps"
#   in API group "" in the namespace "default": access denied
```

That is the whole model working end to end: a password became a token,
the token became a list of workspaces, the list became a kubeconfig, and
kcp enforced the role — with no privileged credential anywhere along the
way.

---

## 5. The marketplace: publish an API, enable it as a user

The marketplace is the other half of the stack, and it composes with
tenancy without either component naming the other: a provider publishes an
API, and a consumer — *any* workspace, including the project alice just
got a kubeconfig for — enables it by creating an ordinary kcp APIBinding.

### Register a provider

A `Provider` record in the catalog makes the operator provision a
workspace with the `provider` WorkspaceType, which carries the
`marketplace-publish` binding:

```sh
KC=.tilt/tilt-frontproxy.kubeconfig
CATALOG=https://kcp.localhost:8443/clusters/root:marketplace:catalog

kubectl --kubeconfig $KC --server $CATALOG apply -f - <<'EOF'
apiVersion: marketplace.platform.kcp.io/v1alpha1
kind: Provider
metadata:
  name: acme
spec:
  displayName: Acme Provider
  mode: provisioned
EOF

kubectl --kubeconfig $KC --server $CATALOG \
  get providers acme -o jsonpath='{.status.phase} {.status.workspacePath}'
# Ready root:marketplace:providers:acme
```

### Publish an offering

Everything in this section is also available as a one-liner —
`tilt trigger marketplace-examples` applies
[the examples](../../marketplace/config/examples/), which publish the same
`things` export plus a second one that claims nothing. It is an ad-hoc job:
it never runs on its own, because publishing grants `bind` to every
authenticated user in this stack. Doing it by hand, once, is worth it the
first time:

Now act as the provider, inside that workspace. The provider brings their
own APIExport — the marketplace never defines what a provider serves, only
how it is offered:

```sh
PROVIDER=https://kcp.localhost:8443/clusters/root:marketplace:providers:acme

kubectl --kubeconfig $KC --server $PROVIDER apply -f - <<'EOF'
apiVersion: apis.kcp.io/v1alpha1
kind: APIResourceSchema
metadata:
  name: v1alpha1.things.acme.example
spec:
  group: acme.example
  names: {kind: Thing, listKind: ThingList, plural: things, singular: thing}
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
  # The offering is the export: these annotations publish it, and its own
  # permissionClaims (none here) are the consent form.
  annotations:
    marketplace.platform.kcp.io/publish: "true"
    marketplace.platform.kcp.io/display-name: "Things as a service"
    marketplace.platform.kcp.io/description: "One Thing per workspace, batteries included."
spec:
  resources:
    - name: things
      group: acme.example
      schema: v1alpha1.things.acme.example
      storage:
        crd: {}
EOF
```

Publishing is what grants `bind`, and nothing else does. Once the operator
sees the annotation it materializes exactly one grant next to the export:

```sh
kubectl --kubeconfig $KC --server $PROVIDER get clusterroles,clusterrolebindings \
  | grep marketplace:bind
```
```
clusterrole.rbac.../marketplace:bind:things.acme.example
clusterrolebinding.rbac.../marketplace:bind:things.acme.example:consumers
```

### Browse the catalog

The marketplace virtual workspace serves the catalog read-only to any
authenticated caller. Each entry carries the coordinates of the binding
you would write, so browsing it is reading both the consent form and the
APIBinding:

```sh
kubectl --kubeconfig .tilt/tilt-frontproxy.kubeconfig get --raw \
  /services/marketplace/apis/marketplace.platform.kcp.io/v1alpha1/offerings
```
```json
{
  "kind": "CatalogOfferingList",
  "items": [
    {
      "metadata": {"name": "acme.things"},
      "provider": "acme",
      "offering": "things",
      "displayName": "Things as a service",
      "export": "things.acme.example",
      "workspacePath": "root:marketplace:providers:acme",
      "revision": "",
      "ready": true
    }
  ]
}
```

### Enable it as an ordinary user

Here the two components meet. Alice uses **the kubeconfig from
[step 4](#4-use-the-kubeconfig)** — her own Dex token, her own project —
and enables the offering herself. Enabling *is* the APIBinding; there is
no marketplace API to call, and no privileged component involved:

```sh
kubectl --kubeconfig /tmp/alice.kubeconfig apply -f - <<'EOF'
apiVersion: apis.kcp.io/v1alpha2
kind: APIBinding
metadata:
  name: things
spec:
  reference:
    export:
      path: root:marketplace:providers:acme
      name: things.acme.example
EOF
# apibinding.apis.kcp.io/things created

kubectl --kubeconfig /tmp/alice.kubeconfig get apibinding things -o jsonpath='{.status.phase}'
# Bound
```

kcp allowed this because two independent things were true: alice may
create APIBindings in that workspace (her tenancy `admin` Membership), and
the export's workspace grants `bind` (the offering is published). Remove
either and it fails.

The API is then hers to use, in her own project:

```sh
kubectl --kubeconfig /tmp/alice.kubeconfig apply -f - <<'EOF'
apiVersion: acme.example/v1alpha1
kind: Thing
metadata:
  name: hello
spec:
  message: "from alice's project"
EOF
# thing.acme.example/hello created

kubectl --kubeconfig /tmp/alice.kubeconfig get things
# NAME    AGE
# hello   1s
```

Bob, `view` on the same project, cannot enable anything — the tenancy role
decides, and kcp enforces it:

```sh
kubectl --kubeconfig /tmp/bob.kubeconfig apply -f - <<'EOF'
apiVersion: apis.kcp.io/v1alpha2
kind: APIBinding
metadata:
  name: things-by-bob
spec:
  reference:
    export:
      path: root:marketplace:providers:acme
      name: things.acme.example
EOF
# Error from server (Forbidden): apibindings.apis.kcp.io is forbidden:
#   User "bob@team-b.example.com" cannot create resource "apibindings"
#   in API group "apis.kcp.io" at the cluster scope: access denied

kubectl --kubeconfig /tmp/bob.kubeconfig get things
# NAME    AGE          ← but he reads what alice enabled, as `view` should
# hello   11s
```

Note what never happened: no component wrote into alice's project, no
marketplace API or WorkspaceType exists there, and nobody relayed her
identity. "Who may enable what, where" was answered entirely by kcp, from
her Membership's RBAC and the provider's `bind` grant.
