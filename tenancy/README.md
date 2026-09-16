# Tenancy

A light tenancy model for [kcp](https://github.com/kcp-dev/kcp): three
cluster-scoped resources — **Tenant**, **Project**, **Membership** — that an
operator turns into real kcp workspaces and RBAC, and one virtual resource —
**SelfTenancyReview** — that answers "which tenants and projects am I in, and
as what role?" in a single call.

This is a clean-room implementation, written for this repository against the
[virtual-workspace-framework](https://github.com/kcp-dev/virtual-workspace-framework)
and the patterns the [access component](../access/) established.

## The model

A platform team runs `tenancy-vw init` once. It creates
`root:tenancy:controllers`, installs the APIResourceSchemas, the
`tenancy.contrib.kcp.io` APIExport, the RBAC that makes it bindable, and the
APIExportEndpointSlice both server processes follow.

An **organization workspace** is any workspace that binds the export (see
[config/examples/apibinding-consumer.yaml](config/examples/apibinding-consumer.yaml)).
The one permission claim — `tenancy.kcp.io` workspaces — must be accepted: it
is what lets the operator create workspaces next to the objects that ask for
them.

Inside an organization workspace:

- A `Tenant` asks for an isolated workspace. The operator provisions one
  under the organization workspace, named after the display name by the
  configured strategy, and reports name, logical cluster and URL in status.
- A `Project` asks for a workspace nested under its tenant's workspace.
- A `Membership` grants one subject (`User` or `Group`, matched verbatim
  against the identity kcp authenticates) one role (`view`, `edit`,
  `admin`) in the tenant's workspace — or in one project's, when
  `spec.project` is set. The operator materializes a ClusterRole and a
  ClusterRoleBinding in the target workspace; every role carries kcp's
  workspace-content `access` verb, since resource permissions are useless in
  a workspace the subject cannot enter. Deleting the Membership removes the
  binding again (a finalizer makes sure of it).

Tenant-wide grants reach every project workspace by nesting: project
workspaces live under the tenant's, so anyone who can enter the tenant
workspace resolves project paths through it, and `SelfTenancyReview` reports
tenant-wide roles on every project.

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
comes from an in-memory directory mirrored from all organization workspaces
(multicluster-runtime over the APIExport's endpoint slice — the same
machinery the access VW uses for RBAC), and is refused with 503 until the
initial sync completes rather than answered partially.

Authentication mirrors the access VW: OIDC/JWT, forwarded request-header
identity, or client certificates; the configuration must match kcp's,
because subjects are compared verbatim.

## Running it

```sh
tenancy-vw init --kubeconfig admin.kubeconfig
tenancy-vw operator --kubeconfig admin.kubeconfig --workspace-path root:tenancy:controllers
tenancy-vw virtualworkspace --kubeconfig admin.kubeconfig \
  --workspace-path root:tenancy:controllers \
  --client-ca-file ca.crt --requestheader-client-ca-file requestheader-ca.crt
```

One binary, three subcommands, one image
(`ghcr.io/kcp-dev/contrib-virtual-workspaces/tenancy-vw`): a deployment's
init container and both long-running processes share the artifact.

## Testing

```sh
make -C .. test           # unit tests (naming, directory, RBAC shapes, storage)
make -C .. test-e2e-tenancy   # full stack: real kcp + operator + VW as local processes
```

The e2e harness ([hack/ci/run-e2e-tests.sh](hack/ci/run-e2e-tests.sh))
builds kcp from a sibling checkout (`KCP_DIR`, default `../../kcp`) or
extracts it from `KCP_IMAGE` in CI, runs `init` twice so idempotency is
exercised on every run, and mints a throwaway client CA whose certificates
(`alice` in `team-a`, `bob` in `team-b`, `mallory`) are what the
SelfTenancyReview scenarios authenticate with.

## Limits, stated plainly

1. **The operator needs admin below the organization workspaces.** Tenancy
   objects and tenant Workspace objects are reached through the APIExport
   virtual workspace, but nested project workspaces and RBAC are written
   through the workspace URLs kcp reports, as the operator's own kubeconfig
   identity. Run it with a credential that is admin of the subtree.
2. **Roles are fixed.** `view`, `edit`, `admin` — no custom role references.
   That keeps a Membership reviewable at a glance; a deployment wanting more
   defines it outside this component.
3. **Workspace readiness is polled** (3s), not watched: readiness lives on
   Workspace objects in claimed clusters, and a poll is simpler than
   engaging every provisioned cluster for one phase field.
4. **`SelfTenancyReview` trusts its directory**, which follows informers; a
   just-created Membership can take a moment to appear in answers.
