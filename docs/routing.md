# Routing

**This describes the fallback, not the current design.** It applies when running
against a kcp without reference-driven replication and endpoint shard
selectors — see *What this needs from kcp* in the [README](../README.md).

With those, a provider publishes the URL of their own virtual workspace into the
endpoint slice their APIExport references, this server answers on a path of its
own (`/services/ephemeral-buckets/...` rather than `/services/apiexport/...`),
and none of what follows is needed: no router, no shard-global URL to move, no
collision to design around.

Without them, the only URL a shard will accept is one composed from
`shard.spec.virtualWorkspaceURL`, which means pointing that shard-global setting
somewhere this server can be reached and splitting the traffic back out by path.
That is what this document is about.

## Why it is needed

When a consumer creates a `BucketInfo` in their own workspace, the shard:

1. finds the `APIBinding` and sees the bound resource has zero storage versions,
   so it is virtual rather than CRD-backed;
2. reads `spec.resources[].storage.virtual.reference` off the `APIExport` and
   resolves the referenced `APIExportEndpointSlice` **through the cache server**;
3. takes a URL out of that slice's `status.endpoints[]` — but only one whose
   prefix matches the shard's own `--shard-virtual-workspace-url`
   (`endpointslice.FindOneURL` is a plain `strings.HasPrefix`);
4. reverse-proxies the request to `<that URL>/clusters/<consumer>` + the
   original API path.

Step 3 is the constraint. A shard will not proxy to `https://ephemeral.example.com`,
however correct that URL is, because it does not carry the prefix the shard was
configured with. So this server cannot simply advertise its own address.

The fix is to make the shard's virtual workspace address a router.

## The shape

```
                     Shard.spec.virtualWorkspaceURL
                                 |
                                 v
                    +------------------------+
   shard  --------> |     routing proxy      |
                    +------------------------+
                       |                  |
   /services/apiexport/<cluster>/         |  everything else
   s3.example.com/**  |                   |
                      v                   v
        ephemeral virtual workspace   kcp virtual-workspaces
             (this server)                  (built-in)
```

Point `--shard-virtual-workspace-url` (or `Shard.spec.virtualWorkspaceURL`) at
the proxy, and route by the path segment that identifies the ephemeral APIExport.

## Envoy / Ingress example

Matching on the export name is the simplest reliable rule, which is why the
example manifests dedicate an `APIExport` to ephemeral resources.

```yaml
# Envoy route configuration, abridged.
routes:
- match:
    safe_regex:
      regex: '^/services/apiexport/[^/]+/s3\.example\.com(/.*)?$'
  route:
    cluster: ephemeral-virtual-workspace
    timeout: 35s          # must exceed the webhook timeout cap of 30s
- match:
    prefix: /
  route:
    cluster: kcp-virtual-workspaces
```

nginx ingress equivalent:

```yaml
metadata:
  annotations:
    nginx.ingress.kubernetes.io/use-regex: "true"
    nginx.ingress.kubernetes.io/proxy-read-timeout: "35"
spec:
  rules:
  - http:
      paths:
      - path: /services/apiexport/[^/]+/s3\.example\.com
        pathType: ImplementationSpecific
        backend:
          service:
            name: ephemeral-virtual-workspace
            port:
              number: 6454
      - path: /
        pathType: Prefix
        backend:
          service:
            name: kcp-virtual-workspaces
            port:
              number: 6444
```

## Do not overlap

This section is the clearest illustration of why the fallback is a fallback.


The built-in apiexport virtual workspace serves the *same* path for the
provider's own access to the export. If you route a whole export here, the
provider loses that access for that export.

That cost is only zero when the export has nothing else in it. The example
manifests do **not** take that option: `s3.example.com` exports a CRD-backed
`Bucket` next to the ephemeral `BucketInfo`, precisely because that is the
realistic shape, and routing the whole export would cost the provider its own
view of consumers' buckets. Two ways out:

- **Route by API group**: narrow the regex to
  `/services/apiexport/[^/]+/[^/]+/clusters/[^/]+/apis/s3\.example\.com/`.
  More precise, but discovery requests for the group land on a different path
  shape, so test both before relying on it.
- **Dedicated export**: put ephemeral resources in an export of their own, and
  the routing rule becomes trivial again. The cost is that consumers bind two
  exports for what is conceptually one API.

Neither has been exercised: `hack/local-up.sh` sidesteps routing entirely by
pointing the shard's virtual workspace URL straight at this server, which also
means the built-in apiexport virtual workspace is unreachable there. That is
survivable in the demo only because nothing in it uses that access.

## Verifying

From a consumer workspace, after applying the example manifests:

```bash
# The verb list must contain create and nothing else.
kubectl get --raw /clusters/<consumer>/apis/s3.example.com/v1alpha1 | jq '.resources[].verbs'
# ["create"]

kubectl create -f docs/example/bucketinfo.yaml -o yaml
# status.sizeBytes and status.objectCount come back populated

kubectl get bucketinfos
# error: the server doesn't have a resource type "bucketinfos"
```

If the create returns a 500 mentioning "error resolving resource", the shard
could not resolve the endpoint slice: check that the slice has a URL in status
and that the URL's prefix matches the shard's `--shard-virtual-workspace-url`.

If it returns a 404, the routing rule did not match and the request reached
kcp's built-in virtual workspace server instead.
