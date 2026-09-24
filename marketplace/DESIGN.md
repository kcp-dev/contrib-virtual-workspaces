# Marketplace: providers and offerings for kcp — design

**Status: built and green** (`make test-e2e-marketplace`, 9 scenarios
against a real kcp). This document is the contract the code follows, in
the same idiom as [tenancy](../tenancy/): capability APIExports, a
WorkspaceType that carries bindings, an operator with no admin client, one
small read-only virtual workspace.

It is a clean-room redesign of the provider/marketplace model in
[railgrid](https://github.com/railgrid/railgrid), which is itself
kcp-based: there, providers publish APIExports, a `CatalogEntry` is the
offering, and "enable" means an `APIBinding` lands in the tenant
workspace. What we keep is that shape — **the catalog is records, the
order is an APIBinding, provisioning is the provider's own controllers
over its export's virtual workspace**. What we push further than railgrid
is *who* creates that binding: nobody privileged. Enabling is kcp itself.

## The one idea

Publishing an Offering materializes exactly one grant in the provider's
workspace: the `bind` verb on that offering's APIExport, to the configured
consumers (default `system:authenticated`). Enabling is then a **plain kcp
APIBinding the consumer authors in their own workspace**:

- kcp checks the `bind` grant in the provider's workspace — so only
  published offerings are enableable, and deleting the Offering revokes
  enablement for every new consumer (the shard admin excepted:
  system:masters bypasses authorization, here as everywhere);
- creating the binding needs the consumer's own RBAC in their own
  workspace — which is exactly what a tenancy `admin` Membership
  materializes, so "who may enable where" is the tenancy system's answer,
  enforced by kcp natively;
- the claims the offering asks for are **accepted on the consumer's own
  object**. That acceptance is the consent, first-class in kcp; a
  provider widening their claims bumps a catalog revision but cannot
  touch a single existing binding, structurally.

The marketplace therefore has **zero footprint in consumer workspaces**:
no bound export, no WorkspaceType, no operator reach. Consumers are
workspace-type agnostic by construction.

## The model

```
root
└── marketplace
    ├── controllers ..................... what init installs, bound by nobody
    │     APIExport marketplace-catalog ....... exports Providers
    │     APIExport marketplace-publish ....... exports Offerings; claims RBAC
    │                                           writes, apiexports + slices
    │                                           (read), logicalclusters (read)
    │     APIExport marketplace-provisioner ... NO resources; claims workspaces
    │     APIExportEndpointSlice × 3
    │     WorkspaceType provider .............. no universal: no tenancy.kcp.io,
    │                                           no topology.kcp.io
    │
    ├── catalog ........................ the registry      (--catalog-workspace)
    │     APIBinding marketplace-catalog ...... bound by init, by hand
    │     Provider "acme" ..................... the record, and only the record
    │
    └── providers ...................... provisioning parent (--providers-workspace)
          APIBinding marketplace-provisioner .. bound by init, by hand
          │
          └── acme ...................... WorkspaceType: provider
                APIBinding marketplace-publish  ... from the WorkspaceType
                APIExport things.acme.example ..... the provider's OWN export
                Offering "things" ................. published by the provider
                ClusterRole/CRB: bind → consumers . materialized by publishing

any workspace (a tenancy project, anything)
      APIBinding things.acme.example ... created by the CONSUMER; this IS
                                         enabling, and kcp checks the bind
                                         grant above on the way
```

**Kinds** (group `marketplace.contrib.kcp.io/v1alpha1`, all cluster-scoped):

- `Provider` — the registration record in the catalog: which workspace may
  publish under this name. `mode: provisioned` makes the operator create
  the workspace (via the workspaces claim, identityHash and all);
  `mode: external` names a workspace the provider brings (BYO). The
  record grants nothing by existing.
- `Offering` — the catalog card, in the provider's workspace, next to the
  APIExport it names. `spec.claims` is a request; the operator validates
  it against the export and records the intersection in
  `status.appliedClaims` at `status.revision`, which bumps on every
  change — widening or narrowing alike, because telling them apart would
  mean the operator deciding which permissions are harmless.
- `CatalogOffering` — virtual, read-only, served by the VW: the offering
  joined with its provider, named `<provider>.<offering>`, carrying the
  bind coordinates (`workspacePath`, `export`) and the claims at the
  current revision. Browsing the catalog is reading both the consent form
  and the APIBinding you would author.

**Registration consent**: a provider workspace is any workspace where
`marketplace-publish` is bound — by the `provider` WorkspaceType for
provisioned providers, by the owner's own hand for BYO. kcp auto-accepts
the export's claims on binding, which is precisely the operator's reach
there, declared, auditable, and revoked by deleting the binding. The
publish reconciler matches the workspace's canonical path (read from its
LogicalCluster, never from anything the owner could claim) against the
Provider records; a workspace no record names publishes into the void.

**The operator** runs two managers (catalog, publish) and one client-only
claim (provisioner). It holds no admin client and never touches a
consumer workspace:

| Through | It can | And therefore cannot |
|---|---|---|
| `marketplace-catalog` | read/write Providers in the catalog | see any Offering or consumer |
| `marketplace-publish` | read Offerings + exports, write bind-RBAC, in provider workspaces | touch anything a provider didn't bind it into |
| `marketplace-provisioner` | create workspaces under the providers parent | create a workspace anywhere else |

**The virtual workspace** serves the catalog read-only at
`/services/marketplace` (plus `/debug/directory`), fed by two exports and
503 until both synced. It writes nothing, anywhere.

## The pluggability contract with tenancy

The two components never name each other. The whole interface is kcp:

1. **A consumer is any workspace.** No marketplace binding, type or claim
   exists there; whoever can create APIBindings in a workspace can enable
   in it. Under tenancy that is an `admin` Membership; under anything
   else, whatever that system materializes.
2. **A provider is any workspace where `marketplace-publish` is bound** —
   including a tenancy project, which is BYO for free.
3. **Subjects are compared verbatim** under the shared
   `AuthenticationConfiguration` ([hack/dex](../hack/dex/)).
4. **Authorization is kcp's own**: the workspace's RBAC for creating the
   binding, the provider workspace's `bind` grant for referencing the
   export. Nothing is relayed because nothing needs to be.

## Deviations from railgrid, stated plainly

1. **No hub, no privileged enabler at all.** Railgrid's enable endpoint
   runs as kcp-admin; here the consumer's own credential does the one
   write enabling consists of.
2. **No heartbeats, versions, or health probes.** Readiness is kcp state.
3. **No UI, MCP, actions, or data plane.** The [mcp](../mcp/) component is
   the natural place to later aggregate enabled providers' tools.
4. **No Grant kind, no minted ServiceAccounts, no per-org catalogs.**
5. **The instance layer is a provider, not the marketplace.**

## History: the operator-mediated enablement that was built and removed

The first implementation carried two more exports and a write path: an
`Enablement` kind in consumer workspaces, a capability-only
`marketplace-binder` export claiming `apibindings.apis.kcp.io` through
which the operator materialized the binding, and a VW write path that
authorized each request by relaying a `SubjectAccessReview` into the
target workspace as the caller. All of it worked — the suite proved the
claim composition, the revision-consent flow and the SAR relay end to
end — and all of it was removed in favor of native bind, which needs
none of it: fewer exports, zero consumer footprint, and consent that
lives on the consumer's own object instead of in operator bookkeeping.

Two artifacts of that phase remain deliberately:

- `make test-spike-marketplace` ([test/spike](test/spike/)) still proves
  the binder-claim + bind-grant composition against a real kcp — the
  evidence that an operator-mediated enabler (self-service for callers
  with no workspace credential, approval flows) can be layered back on
  top of native bind without changing the model underneath.
- The hard-won kcp facts: an `apiexports/content` grant is useless
  without the workspace `access` verb beside it; the `bind` check needs
  no such access in the provider workspace; claimed `apibindings` writes
  are allowed when claim verbs intersect, while unclaimed ones fall back
  to read-only.

## What a CLI adds (not built)

`marketplacectl` would be sugar over exactly the surfaces above:
`catalog` lists the VW's entries; `enable <provider>.<offering>
--workspace <path>` reads one entry and authors the APIBinding — claims,
identity hashes and selectors copied from the catalog — as the calling
user, against kcp. No new server surface is needed, which is the point.
