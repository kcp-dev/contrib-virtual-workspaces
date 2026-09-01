# Ephemeral resources virtual workspace

A kcp virtual workspace that serves **non-persisted resources**: a client `POST`s
an object, a provider's webhook answers synchronously, and the answer is returned
as the response body. Nothing reaches etcd. Only `create` is served.

This is the shape `SubjectAccessReview`, `SelfSubjectReview` and `TokenReview`
already have inside Kubernetes — the client asks a *question* and wants an
*answer*, not a record — made available to anyone extending kcp's API surface.

It is a proof of concept for [kcp-dev/enhancements#15][kep]. The virtual
workspace, the endpoint slice kind it publishes into, and the example webhook
all live here, out of tree and behind no kcp fork. We have set of PRs in kcp
to enable this behaviour.

[kep]: https://github.com/kcp-dev/enhancements/pull/15
 
## Status

This POC depends on:
https://github.com/kcp-dev/kcp/pull/4302 

https://github.com/kcp-dev/kcp/pull/4303 

https://github.com/kcp-dev/kcp/pull/4304

https://github.com/kcp-dev/kcp/pull/4305

## How a request flows

```
kubectl create -f bucketinfo.yaml
  |
  v
consumer workspace on a kcp shard
  |  RBAC on create bucketinfos, admission, schema validation
  |  APIBinding says: virtual storage, zero storage versions
  |  resolve EphemeralResourceEndpointSlice -> URL, reverse-proxy
  v
this server, at the URL it published itself
  |  authorize: create only, cluster must bind the export
  |  POST EphemeralReview to the provider's webhook
  |  validate the answer against the APIResourceSchema
  v
201 with the webhook's object, never stored
```

## The webhook contract

kcp `POST`s an `EphemeralReview` and expects one back — modelled on
`AdmissionReview`, so most of the plumbing carries over:

```yaml
apiVersion: ephemeral.contrib.kcp.io/v1alpha1
kind: EphemeralReview
request:
  uid: 8f16193c-4b5c-4d9e-b4ed-210b3798917d
  cluster: consumer-ws                  # the consumer's logical cluster
  resource: {group: s3.example.com, version: v1alpha1, resource: bucketinfos}
  kind:     {group: s3.example.com, version: v1alpha1, kind: BucketInfo}
  userInfo: {username: alice, groups: [team-a]}
  dryRun: false
  object: {...}                         # what the client submitted
```

```yaml
apiVersion: ephemeral.contrib.kcp.io/v1alpha1
kind: EphemeralReview
response:
  uid: 8f16193c-4b5c-4d9e-b4ed-210b3798917d   # must echo the request
  allowed: true
  object: {...}                               # returned to the client as the 201 body
  # or, when allowed: false
  status: {code: 404, reason: NotFound, message: "bucket does not exist"}
```

Rules the server enforces:

- **The answer is validated** against the resource's OpenAPI schema. A broken or
  hostile webhook cannot smuggle arbitrary content through a typed API.
- **`allowed: false` becomes a real API error**, so a provider says "no such
  bucket" as a `404` rather than an opaque failure. Always reply HTTP 200; the
  denial travels in `response.status`.
- **The UID must be echoed.** A mismatch is a webhook failure.
- **No retries, no caching, no failure policy.** The webhook is authoritative on
  every request. There is deliberately no `Ignore` policy: returning the
  submitted object unchanged is indistinguishable from a real, empty answer.
- **`dryRun` is passed through.** A webhook with side effects must not perform
  them when it is set.

[`examples/webhook`](examples/webhook) is a working reference implementation.

## Running it

### Against an existing kcp

First, a kcp binary you can run from this directory. Make target will pull in
binary from local copy of the kcp repo, or you can build it yourself. The kcp 
binary must carry the PRs above.

```bash
make kcp          # -> bin/kcp, built from ../kcp (override with KCP_DIR)
make kubectl-ws   # -> bin/kubectl-ws, if you want `kubectl ws`
```

Running from this directory is the point: every path below — `pki/`, the config
file, `.local/` — is relative to it.

That kcp needs the changes the PRs above carry. Without them a shard will not
resolve a reference to a provider-owned kind, and the resource disappears from
discovery with the reason only in the shard's log.

**The flags kcp needs:**

| Flag | Why |
| --- | --- |
| `--feature-gates=CacheAPIs=true` | reference-driven replication is gated on it, and it is alpha and off by default. Without it the endpoint slice is never replicated to the cache server, and the failure is indistinguishable from the publishing controller not running |
| `--shard-virtual-workspace-ca-file=pki/ca.crt` | trust this server's serving certificate |
| `--shard-client-cert-file=pki/shard-client.crt` | the identity kcp presents when it proxies, and — with identity forwarding — the certificate that makes the forwarded `X-Remote-*` headers believable |
| `--shard-client-key-file=pki/shard-client.key` | |

`shard.spec.virtualWorkspaceURL` is deliberately **not** in that list. Moving it
is the old approach, and it redirects every virtual workspace on the shard.

The order matters, because the flags point at files derived from kcp's own
output:

Start dev instance to generate the certificates and kubeconfigs
```bash
./bin/kcp start
```

Then run the script to generate the certificates and kubeconfigs
```bash
hack/gen-pki.sh --kcp-root .kcp --out ./pki
```

Run kcp with the generated certificates and kubeconfigs
```bash
./bin/kcp start \
  --feature-gates=CacheAPIs=true \
  --shard-virtual-workspace-ca-file=pki/ca.crt \
  --shard-client-cert-file=pki/shard-client.crt \
  --shard-client-key-file=pki/shard-client.key
```

It reads `admin.kubeconfig` out of that directory and changes nothing in it. On
a kcp that has never started there is no `admin.kubeconfig` yet, so it writes
the certificates, tells you to start kcp, and finishes the kubeconfig when you
run it again.

That two-pass dance — kcp started with flags pointing at files derived from
kcp's own output — is an artifact of `kcp start` being its own certificate
authority, and it does not exist in a deployment where the PKI is provisioned
before anything runs. See
[Why this is circular, and why only here](docs/deployment.md#why-this-is-circular-and-why-only-here).

`gen-pki.sh` writes:

| File | What it is |
| --- | --- |
| `ca.crt`, `ca.key` | your CA. kcp verifies none of these certificates — it only *presents* the shard client one — so no kcp CA configuration changes |
| `vw.crt`, `vw.key` | this server's serving certificate; kcp trusts it via `--shard-virtual-workspace-ca-file=pki/ca.crt` |
| `shard-client.crt`, `.key` | the identity kcp presents when it proxies, authenticated here by `--client-ca-file=pki/ca.crt`. Not `system:masters`, or this server's own authorizer is short-circuited |
| `vw-client.kubeconfig` | this server's own client to kcp, using the `shard-admin` token from `admin.kubeconfig`. It has to be that one: the informers watch every logical cluster at once and kcp refuses wildcard list/watch to `kcp-admin` |
| `kcp-ca.crt` | kcp's serving CA, extracted from `admin.kubeconfig` |
| `webhook.crt`, `.key` | for the example webhook only; a real provider brings its own |
| `ephemeral-config.yaml` | a starter copy of [docs/example/ephemeral-config.yaml](docs/example/ephemeral-config.yaml) with `caBundleFile` pointed at the CA above |

Then start kcp with the flags the script prints, and this server with the rest:

```bash
./bin/ephemeral-virtual-workspace \
  --ephemeral-config=pki/ephemeral-config.yaml \
  --secure-port=6454 \
  --virtual-workspace-name=ephemeral-buckets \
  --tls-cert-file=pki/vw.crt \
  --tls-private-key-file=pki/vw.key \
  --client-ca-file=pki/ca.crt \
  --requestheader-client-ca-file=pki/ca.crt \
  --requestheader-allowed-names=kcp-shard \
  --requestheader-username-headers=X-Remote-User \
  --requestheader-group-headers=X-Remote-Group \
  --requestheader-extra-headers-prefix=X-Remote-Extra- \
  --kubeconfig=pki/vw-client.kubeconfig \
  --cache-kubeconfig=pki/vw-client.kubeconfig \
  --authentication-kubeconfig=pki/vw-client.kubeconfig \
  --authentication-skip-lookup \
  --allow-private-webhook-addresses
```

The config file is the registry of webhook-backed resources, and the generated
one is a starting point rather than a finished file: the script cannot know
where the provider's endpoint is, so `webhook.url` still holds the example's
placeholder, and `export`/`group`/`resource` have to match your `APIExport`.
The client certificate lines come commented out — a path to a file that does
not exist stops the server from starting, and whether this server presents a
certificate to the webhook is a decision between you and the provider.

**The flags this server needs**, beyond the obvious serving and kubeconfig ones:

| Flag | Why |
| --- | --- |
| `--client-ca-file=pki/ca.crt` | authenticates the shard when it proxies. Must be the issuer of the shard's `--shard-client-cert-file` — **not** kcp's client CA, which a plain `kcp start` does not have |
| `--requestheader-client-ca-file=pki/ca.crt` | whose forwarded identity to believe: only over a connection this CA signed |
| `--requestheader-allowed-names=kcp-shard` | and only from the shard. Leaving it empty lets anything holding a certificate from that CA assert any user |
| `--requestheader-username-headers=X-Remote-User`, `--requestheader-group-headers=X-Remote-Group`, `--requestheader-extra-headers-prefix=X-Remote-Extra-` | the header names kcp stamps. `--authentication-skip-lookup` is required against kcp, and that is normally where this configuration would come from, so it has to be passed here |
| `--authentication-skip-lookup` | kcp has no `extension-apiserver-authentication` ConfigMap in `kube-system` |
| `--allow-private-webhook-addresses` | development only: loopback is exactly what the SSRF guard refuses |

Omitting the `--requestheader-*` flags is a **silent** downgrade, not an error:
the headers are ignored, the shard's client certificate authenticates instead,
and the request proceeds as `kcp-shard`.

### RBAC, and what identity forwarding removes

A `bucketinfos` create is authorized twice, by two different servers: the shard
checks the caller in the consumer's workspace exactly as it would for any stored
resource, and then this server checks again on the far side of the proxy hop.
The second check is the one that needs a grant, and the only question is whose
name is on it.

| Rule | Where | Subject |
| --- | --- | --- |
| `access` on `/` | provider workspace | `kcp-shard` without forwarding, the caller with it |
| `create` on `apiexports/content` for the export | provider workspace | `kcp-shard` without forwarding, the caller with it |

Nothing is needed in the *consumer's* workspace. The caller already holds
`create` on `bucketinfos` there by having bound the export, and nothing on the
second hop consults that workspace's RBAC — the only thing read from it is the
`APIBinding` list, through this server's own informers rather than as the
caller.

These two rules are the catch, and worth understanding before turning
forwarding on. `--require-export-content-authorization` posts a
`SubjectAccessReview` to the **provider's** workspace for `create` on
`apiexports/content`, naming the requesting user as the subject
([`pkg/virtualworkspace/authorizer/content.go`](pkg/virtualworkspace/authorizer/content.go)).
That user used to be the shard, which the provider could grant once. It is now
every consumer — and a consumer does not normally hold rights in a provider's
workspace at all.

So a provider turning this on has to either grant it to the population of
consumers (`system:authenticated`, or a group), or set
`--require-export-content-authorization=false` and rely on the binding check,
which is what actually establishes that the consumer is entitled to the export.
Whether that check is the right one at all is an open question: kcp expresses
"may this consumer use this export" through the `APIBinding` and
`maximalPermissionPolicy`, not through `apiexports/content`. But its better than
nothing... for now.

The `access` rule is not optional: kcp's workspace content authorizer gates
every other rule behind `verb=access` on `/`, so without it the
`apiexports/content` rule is never consulted and the denial reads as a flat
`access denied`.

One more thing worth knowing when reading a denial: the message is formatted
from the *request's* attributes, so a missing `apiexports/content` rule in the
provider's workspace surfaces as

```
User "..." cannot create resource "bucketinfos"
```

naming the resource that was asked for rather than the permission that was
checked.

The YAML for all of this is in
[Applying the example by hand](#applying-the-example-by-hand).

### APIExportEndpointSlice controller: publishing this server's address

A shard finds this server by reading a URL out of the endpoint slice an
APIExport references, and nothing in kcp writes that URL. A second binary does:

`--virtual-workspace-name` has to match what the virtual workspace was started
with, since it is part of the path being published. When it does not, kcp gets a
message that looks like an authorization failure and is not one:

```
forbidden: User "kcp-shard" cannot get path "/services/ephemeral-buckets/...":
Path not resolved to a valid virtual workspace
```

Nothing was denied — no virtual workspace claimed the path. Compare the URL in
the slice against the `path=` the server logs on startup.

`--external-url` is the address **kcp** dials, not one this server binds, so it
has to resolve from the shard. `https://localhost:6454` is right for everything
on one machine; a deployment puts its real hostname there. Get it wrong and
kcp says so, in the shard's log rather than to the client:

```
failed to perform API discovery: Get "https://ephemeral.example.com:6454/services/...":
dial tcp: lookup ephemeral.example.com: no such host
```

**It is a separate process, and it runs against the provider's workspace** —
the one the APIExport lives in. That is not packaging preference. The slice kind
is a CRD the provider applied to their own workspace, so it is served only
there: there is no cluster-wide list of them to make, and no reason for this to
hold credentials for anything else. The kubeconfig's server URL carries the
workspace (`.../clusters/root:providers:s3`, which is what `kubectl ws use`
writes), so the process is scoped by construction.

The virtual workspace itself is the other way round: it serves every workspace
that binds the export, and needs wildcard access to do so. Two different jobs,
two different scopes, so two processes.

Controller should be topology aware, but in this demo it just writes a single GLOBAL endpoint

What it writes:

```bash
kubectl get ephemeralresourceendpointslice s3.example.com -o yaml
```
```yaml
status:
  endpoints:
  - url: https://localhost:6454/services/ephemeral-buckets/<cluster>/s3.example.com
    shards:
      matchAll: true
```

Three things are worth reading off that:

- **The URL is built, not configured.** `<external-url>/<root-path-prefix>/<virtual-workspace-name>/<cluster>/<export>`,
  so it always agrees with the path this server actually answers on. The cluster
  and export name come from the slice; the rest from this server's own flags.
- **`shards.matchAll` means every shard.** One virtual workspace, whatever the
  installation's topology — which is what a webhook-backed resource wants, since
  there is nothing shard-local about asking a provider a question. A shard picks
  it because an empty selector matches any shard's labels.
- **A slice with no status is the normal starting state.** No kcp controller
  fills this one in. If it stays empty, the controller is not running, is
  pointed at the wrong workspace, or the CRD is not applied there.

`--shards` narrows who uses the URL — `--shards=region=eu` publishes
`shards: {selector: {matchLabels: {region: eu}}}` — and no `--shards` publishes
`matchAll`. The two are different in the API on purpose: `matchAll` is the whole
installation, a selector is a subset of it, and saying neither would ask kcp to
match the URL by prefix against the shard's own address.

**One virtual workspace per shard is not supported. But once can build this.** The controller writes
exactly one endpoint and replaces whatever was there, so two instances would
fight over the same slice rather than each contributing their own. The API
expresses it and `--shards` describes it; what is missing is each instance
owning its own entry in the list.

### Then the objects

That still produces a server that is never called until the objects and RBAC
are in place. **[docs/deployment.md](docs/deployment.md) is the full
procedure**: what kcp itself has to be configured with, which certificate is
trusted by whom, the RBAC the deployment has to add, the order the objects go
in, and a table mapping each failure to its cause.

### Locally, in one command

```bash
make build
hack/local-up.sh          # kcp + webhook + this server + manifests + checks
hack/local-up.sh down     # stop
hack/local-up.sh clean    # stop and wipe .local/
```

It uses `bin/kcp` if `make kcp` has produced one, otherwise `../kcp/bin/kcp`
(override with `KCP_BIN`); either way it has to carry the kcp changes described
in [Against an existing kcp](#against-an-existing-kcp). It calls
`hack/gen-pki.sh` for exactly the files described above, so the demo and the
documented procedure cannot drift apart, and it points
`--shard-virtual-workspace-url` straight at this server so that no router is
needed. That last part is the one thing it does that a deployment must not:
afterwards, no other virtual workspace is reachable through the URL kcp
advertises.

It finishes by showing the advertised verbs, a create answered by the webhook,
a refused `get`, a webhook denial surfacing as a `404`, and a dry-run. Logs land
in `.local/logs/`.

`KCP_PORT`, `VW_PORT`, `WEBHOOK_PORT` and `KCP_V` are overridable. To run
beside a kcp you already have, move `ETCD_CLIENT_PORT` and `ETCD_PEER_PORT` as
well: kcp's embedded etcd binds `2379`/`2380` whatever `--secure-port` says, so
changing `KCP_PORT` alone is not enough.

### Applying the example by hand

What `local-up.sh` does to [docs/example](docs/example), as kubectl commands.

**First check kcp is running with all five flags above.** Two failure modes
follow from getting that wrong, and both are confusing:

- **No `--feature-gates=CacheAPIs=true`** (it is alpha and off by default):
  neither the virtual-resources proxy nor the aggregating discovery is
  constructed, so nothing intercepts the request and the bound CRD serves it.
  The object is written to etcd, `kubectl get` works, and everything below
  appears to succeed while doing the opposite of what this project is for.
- **The gate without `--shard-virtual-workspace-url`**: kcp proxies to itself in
  a loop, as described above.

One command tells you which world you are in:

```bash
kubectl api-resources --api-group=s3.example.com -o wide
# create                                     <- served by this virtual workspace
# create,delete,get,list,patch,update,watch  <- CRD storage; the gate is off
```

Then point `KUBECONFIG` at kcp and use the `kubectl ws` plugin (`bin/kubectl-ws`
in a kcp checkout) to move between workspaces; it rewrites the current context,
so every `kubectl` after it applies to that workspace.

```bash
export KUBECONFIG=.kcp/admin.kubeconfig
kubectl ws .                     # where am I
```

**1. Provider workspaces:**

```bash
kubectl ws :root
kubectl ws create providers --enter
kubectl ws create s3 --enter
```

**2. Schemas and export.** Two schemas: `Bucket`, an ordinary stored resource,
and `BucketInfo`, the ephemeral one. They share an export on purpose — the
thing you own, and a live question about it.

```bash
kubectl apply -f docs/example/01-apiresourceschema.yaml
```

`storage.virtual.identityHash` is required, but the value it wants is the
export's own `status.identityHash`, which does not exist until the export is
admitted. Create it with the placeholder, then patch:

```bash

# Check identity. Its optional but we have still "Required" in upstream. so its a placeholder
kubectl apply -f docs/example/02-apiexport.yaml
```

**3. Endpoint slice.** The object the APIExport references, and the CRD that
defines its kind — a provider-owned kind, because a provider is the only party
that knows where their virtual workspace runs:

```bash
kubectl apply -f docs/example/00-endpointslice-crd.yaml
kubectl apply -f docs/example/03-endpointslice.yaml
kubectl get ephemeralresourceendpointslice s3.example.com -o jsonpath='{.status.endpoints}'
```

Empty is expected here: no kcp controller fills this one in. That is
`endpointslice-controller`'s job — a separate process running against this
workspace — and it writes a single endpoint with an empty `shards` selector,
meaning one virtual workspace for every shard. See
[APIExportEndpointSlice controller](#apiexportendpointslice-controller-publishing-this-servers-address).

**4. RBAC in the provider workspace.** Unlike the consumer-side grant in step 8,
this one does not go away with identity forwarding — only its *subject* changes.
`--require-export-content-authorization` asks the provider's workspace whether
the requesting user may use this export, and forwarding changes who that user
is: `kcp-shard` without it, the consumer with it. Bind the wrong one and the
answer is no. See
[RBAC, and what identity forwarding removes](#rbac-and-what-identity-forwarding-removes).

Below is the subject for a kcp that does **not** forward identity. With
forwarding, replace it with the consumers (or set the flag to `false`).

```bash
kubectl apply -f - <<'EOF'
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata: {name: ephemeral-shard}
rules:
- nonResourceURLs: ["/"]
  verbs: ["access"]
- apiGroups: ["apis.kcp.io"]
  resources: ["apiexports/content"]
  resourceNames: ["s3.example.com"]
  verbs: ["create"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata: {name: ephemeral-shard}
roleRef: {apiGroup: rbac.authorization.k8s.io, kind: ClusterRole, name: ephemeral-shard}
subjects:
- {apiGroup: rbac.authorization.k8s.io, kind: User, name: kcp-shard}
EOF
```

The `verb=access` on `/` rule is not optional: kcp's workspace content
authorizer gates every other rule behind it, and without it the denial is a bare
`access denied` naming the resource rather than the missing grant.

**5. Run a webhook**, because nothing answers the request otherwise. Skip this
and the symptom is `no such host` in this server's log and a `503` at the
client.

```bash
make build     # -> bin/example-webhook

./bin/example-webhook \
  --tls-cert-file=pki/webhook.crt \
  --tls-private-key-file=pki/webhook.key
```

It listens on `:18443`, which is where the generated config already points, and
`pki/webhook.crt` is signed by the `pki/ca.crt` that config names as
`caBundleFile` — so nothing has to be lined up by hand.

**6. Publish where the virtual workspace is.** The slice from step 3 still has
an empty status, and nothing in kcp fills it in:

```bash
cp .kcp/admin.kubeconfig pki/controller-client.kubeconfig
export KUBECONFIG=pki/controller-client.kubeconfig
./bin/endpointslice-controller \
  --kubeconfig=$KUBECONFIG \
  --external-url=https://localhost:6454 \
  --virtual-workspace-name=ephemeral-buckets \
  --export=s3.example.com
```

```bash
kubectl get ephemeralresourceendpointslice s3.example.com -o jsonpath='{.status.endpoints}'
# [{"shards":{},"url":"https://localhost:6454/services/ephemeral-buckets/<cluster>/s3.example.com"}]
```

Until that URL is there the shard has nowhere to forward, and `bucketinfos`
stays absent from discovery.

**7. Register the resource with this server.** In `pki/ephemeral-config.yaml`,
check that `export.path` is `root:providers:s3`, `export.name` is
`s3.example.com` and `group`/`resource` are `s3.example.com`/`bucketinfos`. For
a real provider, replace `webhook.url` — or generate the config with
`hack/gen-pki.sh --webhook-url https://…`.

Then **restart this server**, since the config is read once at startup. Against
the example webhook it also needs `--allow-private-webhook-addresses`, because
loopback is exactly what the SSRF guard refuses by default; a real endpoint does
not need the flag.

**8. Consumer workspace: the same RBAC, then bind.** The grant is again only
for a shard that does not forward identity; with forwarding, the consumer
already has these rights, since they are the ones creating the object.

```bash
kubectl ws :root
kubectl ws create consumer --enter

kubectl apply -f docs/example/04-apibinding.yaml
kubectl get apibinding s3.example.com -o jsonpath='{.status.phase}'   # Bound
```

**9. Verify:**

```bash
kubectl api-resources --api-group=s3.example.com -o wide
# buckets     ... Bucket       create,delete,get,list,patch,update,watch
# bucketinfos ... BucketInfo   create                <- create, and nothing else

kubectl create -f docs/example/bucket.yaml
kubectl get buckets
# my-bucket is there: the CRD-backed half of the same export, stored as usual

kubectl create -f docs/example/bucketinfo.yaml -o yaml
# status comes back from the webhook; nothing is stored

kubectl get bucketinfos
# Forbidden: verb "list" is not served by ephemeral resources, only create is
```

The two sit in one APIExport and behave differently because of one field,
`storage`, in [02-apiexport.yaml](docs/example/02-apiexport.yaml).

If instead the object comes back with a `resourceVersion` and a `uid`, and
`kubectl get` lists it afterwards, the request never reached this server — go
back to the feature gate above and delete what got written, because those are
real etcd objects.

### Notable flags

| Flag | Default | Notes |
| --- | --- | --- |
| `--ephemeral-config` | required | Registry of webhook-backed resources. |
| `--virtual-workspace-name` | `ephemeral` | The path segment served, giving `<external-url>/services/<name>/<cluster>/<export>`. Name it after the service, e.g. `ephemeral-buckets`. |
| `--allow-private-webhook-addresses` | `false` | Development only. See below. |
| `--require-export-content-authorization` | `true` | See [Known gaps](#known-gaps). |

## Security

- **Registration is operator-controlled.** Providers cannot add webhooks; an
  operator edits the config file. This is a consequence of running out of tree,
  and it happens to be the authorization gate the KEP discussion asked for.
- **Private addresses are refused by default.** A webhook endpoint resolving to
  loopback, link-local (`169.254.169.254`), or RFC1918 space is rejected at dial
  time, after DNS resolution, so a public hostname pointing inward is caught too.
  Without this a virtual workspace running in-cluster is a confused deputy.
- **Redirects are never followed.** The review carries the user's identity;
  handing it to an unconfigured host is not acceptable.
- **Response bodies are capped** at 3 MiB, matching what kcp accepts inbound.
- **Timeouts are capped at 30s** regardless of what the config asks for.
- **mTLS to the webhook is supported and recommended.** The review asserts an
  identity, which puts the webhook in an aggregated-API-server position: without
  client authentication, anyone who can reach the endpoint can assert anything.
  Providers should verify the certificate chain, not merely require that one was
  presented. (The review asserts the caller's identity only if the shard
  forwards it and this server is started to trust the forwarding — see
  [known gap 1](#known-gaps). Otherwise it asserts the *shard's*.)
- **Only `create` is authorized**, checked before any lookup, independently of
  which interfaces the storage implements.
- **The consumer must have bound the export**, verified against the `APIBinding`
  in the target cluster. Without this, anyone reaching the endpoint could call a
  provider's webhook on behalf of any workspace name in the URL.

## Images

Published to `ghcr.io/kcp-dev/contrib-virtual-ephemeral-resources-virtual-workspace`
by [`.github/workflows/images.yaml`](.github/workflows/images.yaml), on `v*`
tags only. Every other trigger builds both architectures without pushing, so a
Dockerfile that has stopped compiling fails on the pull request rather than at
release time.

One image carries all three binaries, because a deployment needs two of them in
different places and pulling two artifacts to get them helps nobody:

| Binary | |
| --- | --- |
| `/ephemeral-virtual-workspace` | the server, and the image's entrypoint |
| `/endpointslice-controller` | publishes the server's address, and runs in the provider's workspace rather than beside the server |
| `/example-webhook` | not part of a deployment — the provider's side of the contract, shipped so the request path can be demonstrated from the image alone |

## Testing

```bash
make test            # unit tests
make test-e2e        # stand the whole stack up, run the e2e tests, tear it down
make test-e2e-keep   # same, but leave it running to poke at
```

[`hack/ci/run-e2e-tests.sh`](hack/ci/run-e2e-tests.sh) owns processes,
certificates and ports; the tests in [test/e2e/](test/e2e/) own workspaces,
objects and assertions. They apply the manifests from
[docs/example/](docs/example/) directly rather than copies of them, so the
walkthrough above and the test cannot drift apart.

The tests are behind a `//go:build e2e` tag, so `go test ./...` will not run
them. They need a kcp carrying the changes in
[Against an existing kcp](#against-an-existing-kcp) — none of which are in a
release, so it comes either from a checkout (`KCP_DIR`, default `../kcp`) or,
on Linux, straight out of the image built off main
(`KCP_IMAGE=ghcr.io/kcp-dev/kcp:main`, which is what CI uses and what saves it
several minutes of compiling). The image holds a Linux binary, so a macOS
workstation builds from the checkout;
against a kcp without them the run stops at *"bucketinfos never became
servable"*, which is the shard being unable to resolve the endpoint slice.

The point is the assertions a unit test cannot make. That `bucketinfos`
advertises `create` and nothing else while `buckets` in the same export is
stored and listable. That the URL the shard dialled was the one the controller
published, with `shards.matchAll`, while `shard.spec.virtualWorkspaceURL` was
never set. That the same question twice returns the same answer and no
`resourceVersion`. That a webhook denial arrives as a `NotFound` rather than a
`500`. And that the webhook was told the consumer's identity rather than
`kcp-shard` — the one thing that regresses silently, because everything still
works when it is wrong.

## Known gaps

1. **Identity forwarding works, but rests on how the shard is issued its
   certificate.** The caller now reaches the webhook: with the kcp change and
   the `--requestheader-*` flags, the example webhook logs
   `user="kcp-admin" groups=[system:kcp:admin system:authenticated]` rather than
   `kcp-shard`. Without them the request arrives as the shard's client
   certificate instead, and every decision downstream is about the wrong
   subject.

   Three things about it are still unresolved:

   - **The discovery path still fetches as the shard.** Only the resource proxy
     was changed. Discovery is a fetch on the shard's own behalf and there is no
     caller to attribute it to at that point, which is arguably right, but it
     means the verb list is computed under a different identity than the request
     that follows.
   - **It moves trust onto certificate provisioning.** The shard's proxy
     certificate now decides who may assert an identity. Signing it with the
     same CA used for anything else that dials this server would let that
     something else claim to be any user.
   - **It makes `--require-export-content-authorization` check a subject that
     will usually fail it.** The check was written for a provider-side caller;
     pointing it at consumers asks a provider-shaped question about them. See
     [RBAC, and what identity forwarding removes](#rbac-and-what-identity-forwarding-removes).

2. **Barely tested against a sharded kcp.** One two-shard deployment has been
   run, via kcp-operator behind a `kcp-front-proxy`, and it works — but with both
   the provider and the consumer workspace on the same shard. The case the
   endpoint selector actually exists for, a consumer on a *different* shard from
   the provider, has still not been exercised. That run did surface two failure
   modes worth reading before attempting your own, both in
   [docs/deployment.md](docs/deployment.md): an APIExport created before its
   referenced kind is `Established` wedges silently, and a proxy between the
   shard and this server discards the forwarded identity.

3. **Nothing here has been run against the kcp changes it now depends on.**
   The deployable shape — a provider-owned endpoint slice kind, this server
   publishing its own address into it, and kcp resolving that reference — is
   written on both sides and has never served a request. Until it has, the only
   demonstrated path is the one that repoints `shard.spec.virtualWorkspaceURL`,
   which is shard-global and therefore not a deployment.

4. **The routing configuration is unverified**, and harder than it looks. The
   local script avoids it by pointing the shard's virtual workspace URL straight
   at this server, which a real deployment cannot do. The Envoy and nginx rules
   in [docs/routing.md](docs/routing.md) have not been run, and an L7 router that
   matches on the path necessarily terminates TLS — so it drops the shard's
   client certificate and has to re-originate with one this server trusts. With
   identity forwarding that router becomes the party this server believes about
   who is asking, and it has to be trusted to pass the `X-Remote-*` headers
   through unaltered rather than setting them itself.

   This one is no longer hypothetical. Routing the hop through kcp's own
   `kcp-front-proxy` does exactly the wrong half of it: the proxy strips the
   shard's `X-Remote-*` and re-stamps its own assessment, so this server is told
   the request came from `root` — the shard's certificate common name — and the
   create fails with `User "root" cannot create resource "bucketinfos"`. Which
   is correct behaviour for an edge proxy and fatal for this hop. Worked through,
   with the three ways out and what each costs, in
   [docs/deployment.md](docs/deployment.md#a-proxy-between-the-shard-and-this-server-eats-the-forwarded-identity).

5. **Providers cannot self-register.** Adding a webhook means editing the config
   file and restarting. Closing this gap needs the in-tree change: one CRD kind,
   one entry in kcp's replication list, a URL controller, and a feature gate.

6. **The config file is read once at startup.** Certificate rotation on disk is
   not picked up; restart the server.
# contrib-virtual-ephemeral-resources-virtual-workspace
