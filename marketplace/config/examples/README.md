# Example offerings

A producer and two offerings, for a development stack. Applying them is
the shortest path from an empty catalog to something you can browse and
enable.

| File | Applied to | What it is |
|---|---|---|
| `producer.yaml` | the catalog workspace | the `Producer` record — registration, and nothing else |
| `things.yaml` | the producer's workspace | a schema and a published export, with a claim on configmaps |
| `things-instructions.yaml` | the producer's workspace | the "how to use this" the console shows after enabling `things` |
| `widgets.yaml` | the producer's workspace | the same, claiming nothing |

The two exports are the two shapes worth seeing. `things` carries a
`permissionClaim`, so its catalog entry has a consent form and a revision
identifying it. `widgets` carries none, so enabling it asks for nothing in
the consumer's workspace and its entry has neither.

Note that neither file contains an offering object, because there is no
such kind. The annotations on the `APIExport` are the offering:

```yaml
metadata:
  annotations:
    marketplace.kcp.hub/publish: "true"
    marketplace.kcp.hub/display-name: Things as a service
    marketplace.kcp.hub/description: One Thing per workspace, batteries included.
```

Only the exact string `"true"` publishes; anything else leaves the export
off the catalog. Removing the annotation takes it off again and revokes
the `bind` grant, without touching the export or anyone already bound.

## Applying them

Under Tilt, on demand:

```sh
tilt trigger marketplace-examples
```

Or directly, against any running marketplace:

```sh
KUBECONFIG=.tilt/tilt-contrib.kubeconfig marketplace/config/examples/apply.sh
```

`apply.sh` is idempotent, so editing an example and re-triggering is the
development loop. It is a script rather than one `kubectl apply -f`
because the two halves go to different workspaces, and the producer's
workspace does not exist until the operator has read the record.

Then:

```sh
bin/contribctl offers
bin/contribctl enable acme.things.acme.example
```

Both schemas declare `additionalPrinterColumns`, which is what makes them
presentable to any client that asks for a Table — `kubectl get things`
shows MESSAGE and AGE, and so does the web console, from the same
declaration. `widgets` also carries a `priority: 1` column, so it appears
only under `-o wide` (and behind the console's "wide" toggle): the two
examples between them cover both cases.
