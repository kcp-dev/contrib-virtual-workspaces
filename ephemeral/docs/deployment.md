# Running against an external kcp

[`hack/local-up.sh`](../hack/local-up.sh) is the executable version of this
document for a single-shard kcp on localhost. Everything below is what changes
when kcp is somebody else's, already running, and sharded.

Read [routing.md](routing.md) first if you have not: it is the one piece that
cannot be solved from inside this server, and the local script sidesteps it
rather than solving it.

## What kcp has to be

1. **New enough to carry the RESTMapper version-fallback fix**
   (`pkg/reconciler/dynamicrestmapper/defaultrestmapper_fallback.go`). Without
   it, `storage.virtual.reference` to an `APIExportEndpointSlice` never
   resolves, and the failure is silent: the resource is simply absent from
   discovery, with `no matches for kind "APIExportEndpointSlice"` appearing only
   in the shard's log.

2. **Running with `--feature-gates=CacheAPIs=true`** on every shard that serves
   consumers. The virtual-resources proxy and the aggregating discovery that
   make virtual storage work are both behind that gate; with it off, a bound
   ephemeral resource falls through to CRD storage and quietly behaves like an
   ordinary, stored object.

3. **Configured with a shard client certificate**, because that is what makes
   kcp use its external virtual workspace URL rather than the loopback client.
   The certificate and CA are yours to create — see the worked example below:

   ```
   --shard-client-cert-file=<shard client cert>
   --shard-client-key-file=<shard client key>
   --shard-virtual-workspace-ca-file=<CA that signed this server's serving cert>
   --shard-virtual-workspace-url=<the router, see routing.md>
   ```

   All four together, on every shard, and all four together with the feature
   gate. Miss `--shard-virtual-workspace-ca-file` and the shard cannot verify
   this server; miss the client cert pair and the shard falls back to a loopback
   bearer token that is only valid on its own listener; miss
   `--shard-virtual-workspace-url` and it defaults to the shard's own base
   address, which makes the shard proxy to itself in an unbounded loop — see the
   failure table at the end.

## Who trusts whom

Four separate relationships. Getting them confused is the most common way to
end up staring at a TLS error.

| Direction | Configured by | What the other end needs |
| --- | --- | --- |
| kcp shard → this server (server auth) | `--tls-cert-file`, `--tls-private-key-file` | the shard's `--shard-virtual-workspace-ca-file` must trust the issuer |
| kcp shard → this server (client auth) | the shard's `--shard-client-cert-file`/`-key-file` | this server's `--client-ca-file` must trust the issuer |
| this server → kcp | `--kubeconfig`, `--cache-kubeconfig` | a privileged identity — see below |
| this server → provider webhook | `webhook.caBundleFile`, `webhook.clientCertFile`/`clientKeyFile` in the config file | the webhook must trust the client cert's issuer |

The first two are a CA you create. Note that **kcp never verifies either of
those certificates** — it only *presents* the shard client certificate — so no
kcp-side CA configuration has to change to introduce them. One self-signed CA
covering this server's serving certificate, the shard client certificate and
the webhook's serving certificate is enough, and is what `hack/local-up.sh`
does.

## The kubeconfigs this server uses

```
--kubeconfig=<privileged client to the shard>
--cache-kubeconfig=<privileged client to the cache server>
```

These are ordinary kubeconfigs that you write; nothing hands them to you under
that name. What matters is the identity in them.

**It has to be privileged.** This server's informers watch APIExports,
APIResourceSchemas and APIBindings across *every* logical cluster at once, and
kcp refuses wildcard list/watch to ordinary users:

```
failed to list *v1alpha2.APIExport: apiexports.apis.kcp.io is forbidden:
User "..." cannot list resource "apiexports" in API group "apis.kcp.io" at the cluster scope
```

How you satisfy that depends on the deployment:

- **A plain `kcp start`** has no client-certificate authentication at all — it
  writes only `admin.kubeconfig`, `apiserver.crt`/`.key` and `sa.key`, and
  authenticates by token. Use the `shard-admin` token out of
  `.kcp/admin.kubeconfig`; it is the user behind the `shard-base` and
  `system:admin` contexts, and it is allowed wildcard list/watch. The
  `kcp-admin` token, behind the `root` and `base` contexts, is **not**, and
  produces exactly the error above.

  (If a `.kcp` directory does contain `client-ca.crt`, it was produced by
  `cmd/test-server`, not by `kcp start`.)

- **A real deployment** issues a client certificate with `O=system:masters`
  from the deployment's client CA. kcp's own virtual workspace server is given
  exactly this treatment — see `cmd/sharded-test-server/virtual.go`, which
  issues one as `kcp-vw-<n>` in `system:masters`.

Either way the kubeconfig points at the shard's **base** URL, with no
`/clusters/...` path: this server's clients add the cluster prefix themselves,
and any path baked into the host is stripped.

`--cache-kubeconfig` is not optional once there is more than one shard: the
`APIExport` being served may be owned by a different shard, and it is only
visible on the cache server. It defaults to `--kubeconfig`, which is correct
only for a single-shard deployment.

## Worked example: a dev `kcp start`

Against a kcp you run yourself as `kcp start` (root directory `.kcp`). This is
exactly what [`hack/local-up.sh`](../hack/local-up.sh) automates; run it
verbatim if you would rather read the script.

**1. Generate everything.** One script does the whole of this section's
certificate and kubeconfig work, and prints the command lines at the end:

```bash
hack/gen-pki.sh --kcp-root ~/path/to/.kcp --out ./pki
```

It reads `admin.kubeconfig` out of `--kcp-root` and modifies nothing there. If
kcp has never started, there is no `admin.kubeconfig` yet: it writes the
certificates, says so, and completes the kubeconfig when re-run after step 2.
Existing files are reused, so it is safe to run repeatedly.

What it produces, and why each exists:

- `ca.crt`/`ca.key` — your CA. kcp verifies none of these certificates (it only
  *presents* the shard client one), so nothing about kcp's own PKI changes.
- `vw.crt`/`vw.key` — this server's serving certificate.
- `shard-client.crt`/`.key` — the identity kcp presents when it proxies.
  Deliberately not `system:masters`; see the RBAC section.
- `webhook.crt`/`.key` — for the example webhook. A real provider brings its own.
- `kcp-ca.crt` and `vw-client.kubeconfig` — this server's own client to kcp,
  written in step 3's pass.
- `ephemeral-config.yaml` — a starter copy of
  [`example/ephemeral-config.yaml`](example/ephemeral-config.yaml) with
  `caBundleFile` pointed at the generated CA. Its `webhook.url` points at
  `examples/webhook` on localhost and works as it stands; `--webhook-url`
  replaces it with a real endpoint. The client certificate lines are commented
  out, because a path to a file that does not exist stops the server from
  starting.

Use `--hostnames`/`--ips` if this server is not on localhost, and
`--shard-user`/`--shard-group` to change the proxied identity.

**2. Restart kcp with five extra flags.**

```bash
kcp start \
  --feature-gates=CacheAPIs=true \
  --shard-virtual-workspace-url=https://localhost:6454 \
  --shard-virtual-workspace-ca-file=$PWD/pki/ca.crt \
  --shard-client-cert-file=$PWD/pki/shard-client.crt \
  --shard-client-key-file=$PWD/pki/shard-client.key
```

Pointing the URL straight at this server, rather than at a router, is what
makes this a dev setup: it is the only virtual workspace reachable through the
advertised URL afterwards. kcp's embedded virtual workspaces keep running and
its own APIBindings initializer keeps using them over loopback — but do **not**
add `--run-virtual-workspaces=false`, because that is what makes the
initializer follow the advertised URL instead, and then no workspace ever
leaves `Initializing`.

**3. Run the script again**, now that `admin.kubeconfig` exists, to get
`pki/kcp-ca.crt` and `pki/vw-client.kubeconfig`:

```bash
hack/gen-pki.sh --kcp-root ~/path/to/.kcp --out ./pki
```

The kubeconfig it writes uses the **`shard-admin`** token, not `kcp-admin` —
that is the part worth knowing if you build it by hand instead. Override with
`--kcp-user` if your kcp's admin.kubeconfig names its users differently.

### Why this is circular, and why only here

Steps 1 to 3 read oddly: kcp is started with flags pointing at files generated
from kcp's own output. That circularity is an artifact of `kcp start` being its
own certificate authority — it mints `admin.kubeconfig` on first boot, and on a
dev machine that file is the only source of an identity privileged enough for
this server's informers.

A real deployment has no such loop, because nothing is derived from a running
kcp:

- **The PKI exists before any component starts.** cert-manager, Vault, an
  installer, whatever issues certificates for the platform issues this server's
  serving certificate and the shards' client certificates from a CA that already
  exists. kcp is handed `--client-ca-file` and
  `--shard-virtual-workspace-ca-file` at first boot, the same as every other
  flag.
- **This server's identity is issued to it, not borrowed.** It gets its own
  client certificate in `system:masters` — see `cmd/sharded-test-server`, which
  does exactly this for kcp's own virtual workspace server — rather than reusing
  a token out of somebody's admin kubeconfig.
- **The virtual workspace URL is known in advance**, because it is a service
  address or an ingress hostname that the deployment chose, not something kcp
  discovers.

So the ordering constraint is: PKI first, then every component, in any order.
The two-pass script exists to work around a dev kcp that generates its own PKI
at runtime, and disappears the moment the PKI is provisioned externally.

**4. Start this server** with the flags in the next section, using
`pki/vw-client.kubeconfig`, `pki/vw.crt`/`.key` and `--client-ca-file=pki/ca.crt`.
Add `--allow-private-webhook-addresses` if the webhook is on localhost.

**5. Apply the objects** in the order below, and add the RBAC.

## Starting this server

Every path below comes from one of the two places established above: `pki/` is
what `hack/gen-pki.sh` wrote, `.kcp/` is kcp's own root directory. None of them
is a file kcp hands you ready-made.

```bash
./bin/ephemeral-virtual-workspace \
  --ephemeral-config=pki/ephemeral-config.yaml \
  --secure-port=6454 \
  \
  `# serving: kcp trusts this via --shard-virtual-workspace-ca-file=pki/ca.crt` \
  --tls-cert-file=pki/vw.crt \
  --tls-private-key-file=pki/vw.key \
  \
  `# authenticating kcp: the CA that signed the shard client certificate,` \
  `# i.e. YOUR ca.crt -- not kcp's client CA, which a "kcp start" does not have` \
  --client-ca-file=pki/ca.crt \
  \
  `# believe the caller identity the shard forwards, over a connection this CA` \
  `# signed and from this name only. Omit to authorize the shard instead.` \
  --requestheader-client-ca-file=pki/ca.crt \
  --requestheader-allowed-names=kcp-shard \
  --requestheader-username-headers=X-Remote-User \
  --requestheader-group-headers=X-Remote-Group \
  --requestheader-extra-headers-prefix=X-Remote-Extra- \
  \
  `# this server's own client to kcp: privileged identity, base URL` \
  --kubeconfig=pki/vw-client.kubeconfig \
  --cache-kubeconfig=pki/vw-client.kubeconfig \
  --authentication-kubeconfig=pki/vw-client.kubeconfig \
  --authentication-skip-lookup
```

`--client-ca-file` is the one worth reading twice. It authenticates the shard
when it proxies, so it has to be the issuer of the shard's
`--shard-client-cert-file` — the CA you generated. It is **not** kcp's client
CA: a plain `kcp start` has none, and in a deployment that does have one, it is
only the right file if that same CA signed the shard client certificates.

`--cache-kubeconfig` points at the cache server in a sharded deployment; with a
single shard it can be the same kubeconfig, as above.

`--authentication-skip-lookup` is required against a real kcp: without it the
delegating authenticator tries to read the `extension-apiserver-authentication`
ConfigMap out of `kube-system`, which is not a thing kcp has. That ConfigMap is
also where requestheader configuration would normally come from, which is why
the `--requestheader-*` flags above have to be passed explicitly.

A shard finds this server through `endpointslice-controller`, a second binary
run against the workspace the APIExport lives in:

```bash
./bin/endpointslice-controller \
  --kubeconfig=pki/provider.kubeconfig \
  --external-url=https://ephemeral.example.com:6454 \
  --virtual-workspace-name=ephemeral-buckets \
  --export=s3.example.com
```

`--external-url` is the address kcp dials, so it has to resolve from the shard —
a service address or ingress hostname in a deployment, `https://localhost:6454`
when everything is on one machine. It writes

```yaml
status:
  endpoints:
  - url: <external-url>/<root-path-prefix>/<virtual-workspace-name>/<cluster>/<export>
    shards:
      matchAll: true
```

into the `EphemeralResourceEndpointSlice`s of that workspace, so the URL always
agrees with the path this server answers on, and the empty selector means every
shard uses it. It is the reason no shard-global setting has to move.

Its kubeconfig is workspace-scoped — a server URL ending in
`/clusters/root:providers:s3` — so unlike this server it needs no wildcard
access, only rights in the provider's own workspace. A slice whose status never
fills in is the first thing to check when a resource does not appear.

`--virtual-workspace-name` is the path segment this server answers on, and it
should name the service it provides — `ephemeral-buckets` serves
`/services/ephemeral-buckets/...`. Whatever it is, `pkg/endpointslice` publishes
a matching URL, so the two cannot drift apart.

## Identity: who the request arrives as

A stock shard proxies with its own client certificate, that certificate wins
over the forwarded bearer token, and so **every authorization decision
downstream of the proxy is about the shard, not about the person who ran
`kubectl`** — [known gap 1](../README.md#known-gaps).

A kcp carrying the identity-forwarding change stamps `X-Remote-User`,
`X-Remote-Group` and `X-Remote-Extra-*` on the proxied request instead. To
accept them, start this server with:

```
--requestheader-client-ca-file=<CA that signed the shard's client certificate>
--requestheader-allowed-names=kcp-shard
--requestheader-username-headers=X-Remote-User
--requestheader-group-headers=X-Remote-Group
--requestheader-extra-headers-prefix=X-Remote-Extra-
```

Omit them and the headers are ignored rather than trusted: the request falls
back to the shard's certificate and the RBAC below is what makes it work. That
is the safe direction to fail, but it is silent — a create that succeeds only
because the shard was granted rights looks identical to one that succeeded on
the caller's.

Two things about that CA deserve care. `--requestheader-allowed-names` is what
stops anything *else* holding a certificate from the same CA asserting an
identity, so it should not be left empty; and the requestheader CA should ideally
not be the CA used for unrelated clients of this server. In
`hack/local-up.sh` it is the same `pki/ca.crt` for both, which is fine for a
single-purpose dev stack and would not be in a deployment.

### A proxy between the shard and this server eats the forwarded identity

Identity forwarding survives exactly one hop. If the URL published into the
endpoint slice points at a proxy rather than at this server — kcp's own
`kcp-front-proxy`, an ingress, anything that terminates TLS and re-originates —
then the `X-Remote-*` headers the shard set are not what arrives here.

kcp's front-proxy is the instructive case, because routing through it is the
natural thing to do when this server runs behind the same hostname as kcp:

```
$ kubectl create -f docs/example/bucketinfo.yaml
Error from server (Forbidden): bucketinfos.s3.example.com is forbidden:
User "root" cannot create resource "bucketinfos" in API group "s3.example.com"
at the cluster scope: access denied
```

`root` is nobody's user account. It is the common name on the shard's client
certificate — proof that the hop discarded the caller and fell back to the
shard, which is the failure mode
[the identity section above](#identity-who-the-request-arrives-as) describes,
arriving by a route that looks like it should have worked.

The shard did its part: kcp stamps `X-Remote-User` with the caller before
proxying. The front-proxy then undid it. Its whole job is to *terminate* client
identity and re-originate — `SetAuthHeaders` strips any inbound `X-Remote-*`
and re-stamps whoever it authenticated, which is the correct and necessary
behaviour for an internet-facing edge proxy. It preserves a forwarded identity
only for callers that pass **request-header** authentication:

- a client certificate signed by its `--requestheader-client-ca-file`, and
- a common name listed in `--requestheader-allowed-names`.

A shard satisfies neither by default. kcp dials virtual workspaces with
`--shard-client-cert-file`, which is issued from the ordinary *client* CA, and
its common name is the shard's (`root`, `shard-alpha`, …) — not the proxy
identity the front-proxy is configured to trust. So the shard authenticates as
an ordinary client called `root`, its headers are stripped, and this server is
told the request came from `root`.

Three ways out, worst to best:

**Trust the shard at the proxy.** Give the front-proxy a request-header CA
bundle that also contains the CA signing the shard client certificates, and add
the shard common names to the allowed names. With kcp-operator:

```yaml
apiVersion: operator.kcp.io/v1alpha1
kind: FrontProxy
spec:
  extraArgs:
    # Bundle of the request-header CA and the client CA. extraArgs are appended
    # after the operator's own flags, and this one is a plain string flag, so
    # the later value wins.
    - --requestheader-client-ca-file=/etc/kcp-front-proxy/shard-requestheader-ca/tls.crt
    # Pass the full list: pflag appends on repeat, so naming only the additions
    # would be fragile if that ever changed.
    - --requestheader-allowed-names=kcp-front-proxy,kcp-mounts-proxy,root,shard-alpha
  extraVolumes:
    - name: shard-requestheader-ca
      secret:
        secretName: frontproxy-shard-requestheader-ca
  extraVolumeMounts:
    - name: shard-requestheader-ca
      mountPath: /etc/kcp-front-proxy/shard-requestheader-ca
      readOnly: true
```

This works — the webhook then reports `observedUser: kcp-admin` rather than
`root`, which is the whole point of that field — and ordinary users are
unaffected, because their common names are not in the allowed list and they fall
through to normal client-certificate authentication.

The cost is real and belongs in the same breath: **the client CA is now also an
impersonation CA** for those common names. Anyone who can get a certificate
issued from it with `CN=root` can set `X-Remote-User` at the edge and become any
user. The client CA also signs every ordinary user certificate, so the two roles
are no longer separated. That is known gap 1's "it moves trust onto certificate
provisioning", in its sharpest form. Acceptable in a development stack; do not
ship it.

**Take the proxy out of the hop.** Publish this server's in-cluster address
in the endpoint slice — `--external-url=https://ephemeral-virtual-workspace.<ns>.svc.cluster.local:6443`
— so the shard reaches it directly, and put the shard common names in *this*
server's `--requestheader-allowed-names` instead. The same trust widening
applies, but scoped to one virtual workspace rather than to the proxy that
fronts everything. Consumers still reach kcp through the front-proxy; only the
shard's internal hop changes.

**Fix the certificate, upstream.** The clean answer is for a shard to hold a
dedicated request-header-signed identity for dialling virtual workspaces,
separate from the client certificate that is its own identity. kcp-operator
already issues exactly that shape for the root-shard proxy
(`CN=kcp-root-shard-proxy`, request-header CA — and already present in this
server's default allowed names). kcp reuses `--shard-client-cert-file` for the
virtual workspace hop instead, so there is nothing to point at yet. Until that
exists, the first two options are what there is.

### RBAC, for a shard that does not forward identity

For a shard authenticating as user `kcp-shard`:

```yaml
# In the consumer's workspace: without this, create fails with
# `User "kcp-shard" cannot create resource "bucketinfos"`.
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: ephemeral-shard
rules:
# kcp's workspace content authorizer gates every other rule behind verb=access
# on "/". A grant without this one is never consulted, and the denial reads as a
# flat "access denied" with no hint that this is what is missing.
- nonResourceURLs: ["/"]
  verbs: ["access"]
- apiGroups: ["s3.example.com"]
  resources: ["bucketinfos"]
  verbs: ["create"]
```

and, in the *provider's* workspace, the same `verb=access` rule plus `create` on
`apiexports/content` for the export — that is what
`--require-export-content-authorization` checks. Set it to `false` if you would
rather not grant the shard identity anything in the provider's workspace; it is
checking the wrong subject either way.

With forwarding in place none of this is needed: the subject is the consumer,
who already holds `create` on `bucketinfos` by virtue of having bound the
export, and the provider-workspace grant is the one
`--require-export-content-authorization` was written to check.

Do not paper over this with `O=system:masters` on the shard client certificate.
The virtual workspace framework's `AlwaysAllowGroups` short-circuits on
`system:masters`, so this server's own authorizer — create-only, binding
required — never runs at all.

## Order of operations

Every object below has a commented example in [`../docs/example`](../docs/example),
numbered in this order.

1. [`00-endpointslice-crd.yaml`](example/00-endpointslice-crd.yaml), the kind
   the reference points at. **This has to be served before the APIExport that
   names it exists** — see [the ordering
   trap](#the-apiexport-must-not-overtake-the-kind-it-references) below, which is
   why the file is numbered `00` and why `hack/local-up.sh` waits here rather
   than applying straight through.
2. [`01-apiresourceschema.yaml`](example/01-apiresourceschema.yaml) in the
   provider workspace — two schemas, a CRD-backed `Bucket` and the ephemeral
   `BucketInfo`. Exactly one version of each must be marked `storage: true`;
   kcp's admission rejects the schema otherwise. The flag is inert for virtual
   storage.
3. [`02-apiexport.yaml`](example/02-apiexport.yaml), which is where the two are
   told apart: `storage.crd` for `buckets`, `storage.virtual` for
   `bucketinfos`. Patch `storage.virtual.identityHash` with the export's
   `status.identityHash` once admitted. Mixing both in one export is fine for
   the request path, and costs something only in routing — see
   [routing.md](routing.md).
4. [`03-endpointslice.yaml`](example/03-endpointslice.yaml), the object itself.
   Its status is left empty: this server fills it in.
5. An entry in this server's config file for `(export, group, resource)` —
   `hack/gen-pki.sh` already wrote `pki/ephemeral-config.yaml` from
   [`example/ephemeral-config.yaml`](example/ephemeral-config.yaml), so this is
   filling in `webhook.url` and the export it applies to — then start or restart
   the server. The file is read once, at startup.
6. [`04-apibinding.yaml`](example/04-apibinding.yaml) in a consumer workspace.

**`status.endpoints` fills in when this server starts**, not when a consumer
binds. Nothing in kcp writes it — that is the difference from an
`APIExportEndpointSlice`, whose URL kcp composes only once an export has
consumers. If it stays empty, the server is not running, has no
`--external-url`, or cannot see the slice.

## Verifying

From a consumer workspace:

```bash
kubectl api-resources --api-group=s3.example.com -o wide
# buckets     ... Bucket       create,delete,get,list,patch,update,watch
# bucketinfos ... BucketInfo   create        <- create and nothing else

kubectl create -f bucketinfo.yaml -o yaml
# status comes back populated, nothing is stored

kubectl get bucketinfos
# Forbidden: verb "list" is not served by ephemeral resources, only create is
```

## When it does not work

| Symptom | Cause |
| --- | --- |
| Resource missing from discovery, `no matches for kind "APIExportEndpointSlice"` in the shard log | kcp predates the RESTMapper fix |
| `kubectl get` works and objects persist | `CacheAPIs` is off, so the resource fell through to CRD storage |
| `forbidden: User "<shard>" cannot get path "/services/...": Path not resolved to a valid virtual workspace` | not an RBAC problem, despite the wording: no virtual workspace claimed that path. The name in the published URL and this server's `--virtual-workspace-name` disagree. Compare the URL in the slice against the `path=` this server logs at startup |
| `failed to perform API discovery: ... dial tcp: lookup <host>: no such host` in the shard log, and the resource missing from discovery | the URL published into the endpoint slice does not resolve from the shard. `--external-url` is what kcp dials, not what this server binds |
| The request hangs and the shard logs `system:apiserver is impersonating system:apiserver` without end | `CacheAPIs` is on but `--shard-virtual-workspace-url` is unset, so it defaults to the shard's own address: the proxy forwards to kcp, kcp's embedded apiexport virtual workspace delegates the same resource back to the shard, and the proxy intercepts it again |
| `error resolving resource; please contact APIExport owner` | the slice has no URL matching the shard's `--shard-virtual-workspace-url` prefix — routing, or no consumer has bound yet |
| `404` from a request that should reach this server | the routing rule did not match and kcp's built-in virtual workspace answered |
| `access denied` with no further detail | a missing `verb=access` on `/` rule, in whichever workspace the check runs against |
| `User "<shard>" cannot create ...` | the RBAC above is missing in the consumer's workspace |
| `no matches for kind "BucketInfo"` in the consumer's workspace, plus `Some resources are temporarily unavailable`, plus `failed to retrieve virtual workspace URL: the server could not find the requested resource` in the shard log every ten seconds — while `buckets` from the same export still works | the APIExport was created before its referenced kind was `Established`, so no `ClusterCachedResource` exists and the endpoint slice never reached the cache server. `kubectl get clustercachedresources` in the provider workspace is empty. See [the ordering trap](#the-apiexport-must-not-overtake-the-kind-it-references) |
| `User "root" cannot create resource "bucketinfos"` — a name that is no user account, only a shard's certificate | a proxy sits between the shard and this server and re-originated the identity. See [a proxy between the shard and this server](#a-proxy-between-the-shard-and-this-server-eats-the-forwarded-identity) |
| `tls: bad certificate`, or an `EOF` calling the webhook | the trust table above, or something else already listening on the webhook's port |
| Informers log `cannot list ... at the cluster scope` | `--kubeconfig` identity is not privileged enough for wildcard list/watch |

## What is still untested

Most of this has been exercised against a single-shard kcp. Cross-shard
resolution should work — `APIExportEndpointSlice` is replicated to the cache
server and the cache client reads across shards — but it has not been run, and
the routing story in particular gets harder with one router in front of several
shards' virtual workspace URLs.

One two-shard deployment has now been run, via kcp-operator with a
`kcp-front-proxy` in front (root and alpha shards, both provider and consumer
workspaces landing on root). It produced the two failure modes documented above
— the ordering trap and the re-originated identity — and served requests
correctly once both were addressed, with the webhook reporting the consumer's
identity rather than the shard's. That is one topology, not coverage: the case
that still has not been run is a consumer workspace on a *different* shard from
the provider, which is the one the endpoint selector exists for.
