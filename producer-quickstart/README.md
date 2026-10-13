# producer-quickstart

A producer, ready to copy. Take this directory, rename three things, and you
have an API on the marketplace: a Go type, the schema kcp serves for it, a
controller that reconciles it in every workspace that enabled it, the
instructions the console shows, a Dockerfile, a deployment manifest, and a
CI workflow. Replace the fake provisioning in the reconciler with real work
and you are done.

It is a template, not a library: nothing in it imports this repository. It
is its own Go module (`example.com/dbaas-producer`) so that the copy builds
on its own, and `make verify` at the root of this repository builds and
tests it so it cannot rot.

The example it ships is a database-as-a-service: a `Database` kind in the
group `dbaas.example.com`, with `engine` and `size` in the spec and a
`phase`, `endpoint` and conditions in the status.

## Quick start

Against the Tilt stack of this repository (`make tilt-up` at the root):

```sh
cp -r producer-quickstart ~/src/my-producer && cd ~/src/my-producer
make generate                                           # deepcopy + APIResourceSchema from apis/
make publish KUBECONFIG=$REPO/.tilt/tilt-contrib.kubeconfig   # register, provision, publish
make run     KUBECONFIG=$REPO/.tilt/tilt-contrib.kubeconfig   # the controller, locally
```

Then enable **Databases** in a project from the console (Producers → Enable)
or `contribctl enable dbaas.dbaas.example.com`, open it in the sidebar, and
create one. It goes `Pending` → `Ready` with an endpoint in a couple of
seconds, in the console's table and in `kubectl get databases` alike.

## Make it yours

Three renames, in this order, then `make generate`:

| What | Where | Example |
|------|-------|---------|
| Module path | `go.mod`, and the `example.com/dbaas-producer/...` imports | `github.com/acme/widgets-producer` |
| API group and kind | `apis/v1alpha1/` (`groupversion_info.go`, `database_types.go`), `config/apiexport.yaml` (`name`, `resources`), `config/apiexportendpointslice.yaml`, `cmd/producer/main.go` (slice default) | group `widgets.acme.com`, kind `Widget` |
| Producer name | `config/producer.yaml`, `config/deploy/deployment.yaml` (`--workspace-path`), `Makefile` (`WORKSPACE_PATH`, `IMAGE`) | `acme` |

`make generate` rewrites `schema:` in `config/apiexport.yaml` to the new
schema's name; everything else that names the kind is listed above.
`grep -rn dbaas` finds whatever is left.

## Layout

| Path | What |
|------|------|
| `apis/v1alpha1/` | The Go types. Markers on them produce the schema, the validation, the defaults, and the printer columns. `zz_generated.deepcopy.go` is generated. |
| `internal/controller/` | The reconciler. `Reconcile` is a plain function over a `client.Client`, so the test drives it with a fake; the multicluster wrapper around it is ten lines. |
| `cmd/producer/` | The binary: a multicluster manager following the export's endpoint slice. |
| `config/` | What gets applied where: `producer.yaml` to the catalog workspace, the rest to the producer's own. `apiresourceschema-*.yaml` is generated. |
| `config/deploy/` | A Deployment for the controller, with the kubeconfig from a Secret. |
| `hack/update-codegen.sh` | controller-gen (deepcopy, CRDs) then kcp's apigen (CRDs → APIResourceSchema), then repoints the export. |
| `hack/publish.sh` | Registers the producer, waits for its workspace, publishes into it, waits for the `bind` grant. |
| `Dockerfile`, `.github/workflows/ci.yaml` | Multi-arch image; CI running `make verify` and a build of the image. |

## How it fits together

1. **Registration.** Whoever runs the platform applies `config/producer.yaml`
   to the marketplace's catalog workspace. The marketplace operator
   provisions a workspace for the producer (`root:marketplace:producers:dbaas`)
   that is already bound to the publish export — registration is being
   vouched for, and is the one step a producer cannot do alone.
2. **Publishing.** Into that workspace go the `APIResourceSchema`, the
   `APIExport`, the `APIExportEndpointSlice` and the instructions
   `ConfigMap` (`hack/publish.sh`). The export's annotations are the
   offering — `publish: "true"`, a display name, a description, the
   instructions' name — so there is no second object to keep in step. The
   operator materializes `bind` on the export for the platform's consumers.
3. **Enabling.** A consumer creates an `APIBinding` in their project, from
   the console or `contribctl enable`. kcp serves the API there; the
   consumer accepts the export's `permissionClaims` on the binding, which is
   how the producer gets reach into anything beyond its own kind.
4. **Reconciling.** The binding puts the project on the endpoint slice. The
   controller follows the slice: every workspace on it becomes a cluster to
   the multicluster manager, requests carry the cluster name, and the client
   it hands back is scoped to that workspace — through the export's virtual
   workspace, with the producer's own credential. The producer never lists
   workspaces and never holds a credential to anything else.
5. **The console.** Nothing to write. The sidebar lists the producer, its
   page shows the instructions (templated with the reader's workspace,
   tenant, project and user) and the kinds it serves; each kind's table has
   the columns from the `printcolumn` markers, the create form is built
   from the schema, and edit is YAML. A kind with a `priority: 1` column
   shows it in the detail view, as `kubectl -o wide` does.

## The reconciler

[`internal/controller/database_controller.go`](internal/controller/database_controller.go)
fakes provisioning: first pass sets `Pending` and requeues, second pass sets
`Ready` and an endpoint. Replace that with the real thing, keeping:

- `status.observedGeneration`, so a spec change after `Ready` is noticed and
  acted on, and a reader can tell a stale `Ready` from a current one;
- the phases and the `Ready` condition, so every client — the console,
  kubectl, an agent over MCP — can tell what is going on without knowing the
  producer;
- `RequeueAfter` for waiting, never sleeping in the reconciler;
- a finalizer (`controllerutil.AddFinalizer` on first sight, teardown then
  `RemoveFinalizer` on deletion) once something exists outside kcp.

The controller only touches its own kind, so the export claims nothing. A
producer that needs the consumer's `ConfigMaps`, or wants to write a
`Secret` with credentials next to the `Database`, declares that in
`config/apiexport.yaml` under `permissionClaims` — and the consumer sees
exactly that list on the catalog card before accepting it.

## Changing the API

Edit the Go types, `make generate`. The schema's name carries a hash of the
shape, so a changed API is a new schema and the export is repointed to it;
kcp moves existing bindings along. That is fine for additive changes with
defaults; for anything else, add a version rather than mutate `v1alpha1`.

## Running it for real

```sh
make image IMAGE=ghcr.io/acme/widgets-producer TAG=v0.1.0
kubectl create namespace dbaas-producer
kubectl -n dbaas-producer create secret generic kcp-kubeconfig --from-file=kubeconfig=./producer.kubeconfig
kubectl apply -f config/deploy/deployment.yaml
```

The kubeconfig's identity needs two things in the producer's workspace:
read the `APIExportEndpointSlice`, and the export's content (the
`apiexports/content` verb) — which the workspace's owner has, and a
dedicated user or ServiceAccount can be granted. The binary takes
`--kubeconfig` (controller-runtime's, so `$KUBECONFIG` and in-cluster
config work too), `--workspace-path` to point it at the producer's
workspace whatever the kubeconfig's current cluster is, and
`--apiexport-endpointslice`.

`make verify` is what CI runs: gofmt, vet, `go mod tidy -diff`, build, the
unit tests, and `make generate` with a clean `git diff` — so a type change
without its generated files fails in CI rather than at publish time.
