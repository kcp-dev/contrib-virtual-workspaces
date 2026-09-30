# The web UI

The console for **kcp Platform** — the tenancy and marketplace components
together: sign in, see the tenants and projects you can reach, browse the
catalog and enable from it, and look at what a workspace actually serves.

## The shape of it

**There is no backend of ours.** The UI is static files that talk to
kcp's front-proxy directly, carrying the user's own OIDC token:

```
/services/tenancy       the SelfTenancyReview — what you can reach
/services/marketplace   the catalog
/clusters/<id>          the project workspace itself: discovery, objects, APIBindings
```

That is the whole API surface, and it is the same one the CLI uses. Two
things follow, and both are the point:

- **The UI cannot show more than the person may have.** Every request is
  authorized by kcp for the identity in the token, so a screen that
  renders is a screen that identity was allowed to read. Hiding the Admin
  menu is a courtesy; the refusal behind it is the control.
- **Nothing here is privileged.** There is no service account, no relay,
  and no server of ours holding a credential — losing this app's files
  loses nobody anything.

Enabling an offering is an `APIBinding` the browser creates as you, which
is exactly what `contribctl enable` does and what the DESIGN calls native
bind. The UI adds no path that did not already exist.

## Running it

`make tilt-up` brings it up with everything else: the `ui-deps` resource
installs the dependencies (`npm ci` against the lockfile) and `ui` runs
the dev server on http://localhost:5173. `UI_ENABLED=false` skips both.

To run it by hand against a stack that is already up:

```sh
cd ui
npm install
npm run dev        # http://localhost:5173
```

Sign in as any of the Dex users — `alice@team-a.example.com`,
`bob@team-b.example.com`, `mallory@strangers.example.com`, password
`password`. Alice is admin of the seeded tenant, bob has `view` on its
project, and mallory is a member of nothing: signing in as each is the
quickest way to see that the screens follow kcp's answers rather than
their own idea of who you are.

**The Admin menu will not appear for any of them**, and that is correct.
It is shown when kcp answers that the identity may create Tenants in
`root:tenancy:store`, and a tenant admin is not a platform admin — alice
is not even allowed to ask the question there, which the UI reads as the
"no" it is. To see that page, sign in as an identity the platform has
granted rights in those workspaces.

### Configuration

`.env.development` carries the defaults for the Tilt stack:

| Variable | Meaning |
|---|---|
| `VITE_KCP_SERVER` | the front-proxy the dev server proxies to |
| `VITE_KCP_INSECURE` | accept its development certificate |
| `VITE_OIDC_ISSUER` | the issuer the browser is sent to |
| `VITE_OIDC_CLIENT_ID` | the public client, `kcp` |

The redirect URI is `<origin>/callback`, and the provider has to allow
it — `hack/tilt/dex.yaml` registers `http://localhost:5173/callback`
next to the CLI's loopback one.

### Why the dev server proxies

kcp sends no CORS headers, so a browser on `localhost:5173` cannot call
`kcp.localhost:8443` directly. The dev server proxies `/services` and
`/clusters` instead, which keeps the browser on one origin. A deployment
does the same thing properly: serve these files behind the same
front-proxy, and the question never arises.

## The pages

| | |
|---|---|
| **Tenants** | the tenants you belong to and your role in each; creating one |
| **A tenant** | one tenant managed: the projects in it, your role, and adding a project |
| **Service Catalog** | the offerings, and enabling one into the selected project |
| **A provider's resource** | whatever an enabled offering serves: list, create, edit and delete it |
| **API Explorer** | discovery for the selected workspace: which groups are served, which kinds, and the objects |
| **Profile** | the identity the servers see — the username a Membership has to name |
| **Admin** | the platform's own records, shown only when kcp answers that you may write them |

Work happens in a project, so the project is the only context the frame
lets you switch: one dropdown in the top bar, grouped by tenant, which is
the UI's `contribctl use`. Every sidebar screen is relative to it.

A tenant is not somewhere you go — no member can enter one, it is where
the platform keeps Projects and Memberships — so it is never a selectable
context. In the dropdown it is a group heading, which HTML makes
unpickable; projects are the only options, and a tenant with none still
appears, saying so.

Tenants is therefore a management view rather than a place: the list shows
what you belong to and creates one, and each tenant has its own page
(`/tenants/<name>`) with the projects inside it and the button that adds
another. That page is the only way into a tenant that has no projects
yet — nothing about it is selectable, so nothing else would reach it.

## Generic CRUD, and where the columns come from

Every enabled offering gets a working screen without the provider writing
a line of UI code, because an offering is an ordinary Kubernetes API once
its `APIBinding` exists. Three things the server already publishes are
enough:

```
GET  /clusters/<id>/apis/<group>/<version>             the resources, and their verbs
GET  /clusters/<id>/openapi/v3/apis/<group>/<version>  the schema
     POST / PUT / DELETE on the collection and items   the writes
```

- **The columns are the provider's.** The list is fetched with
  `Accept: application/json;as=Table;...`, so the server renders the rows
  using the `additionalPrinterColumns` declared on the APIResourceSchema —
  the same path `kubectl` takes for its output. A column with
  `priority > 0` is wide-only and sits behind the "wide" toggle, again as
  `kubectl` treats it. `date` columns carry a raw timestamp, which the
  client renders as an age, again as `kubectl` does.
  A Table is a request the server may decline, so anything other than a
  Table — an ordinary List, or an outright failure — falls back to a plain
  `GET` rendered with Name and Age. Without that fallback a negotiation
  detail shows an empty screen for a resource that plainly has objects in
  it.
- **The buttons are discovery's.** New, Edit and Delete exist only if the
  resource lists `create`, `update` and `delete` among its verbs, so a
  read-only resource gets a read-only screen with nothing to configure.
- **A form for the obvious parts, YAML for the rest.** The editor has two
  tabs. The form is generated from the resource's own OpenAPI schema and
  offers the fields that have an unambiguous control: strings, numbers and
  booleans become inputs, an enum becomes a select, a nested object becomes
  dotted fields, each carrying the schema's `description` as its help text
  and its `default` as the placeholder. Lists and free-form objects get no
  control — there is no single right one, and inventing one would be wrong
  for exactly the providers nobody has seen — so the form names them and
  says to edit them in YAML. The YAML tab is always there and is
  authoritative for the shape as a whole; the two sync when you switch
  tabs, and whichever is open when you press Apply is the one that is read.
  Field derivation is the one piece of real logic here, so it has tests
  (`npm test`).
  The editor hides what cannot be edited and nothing more — `generation`,
  `creationTimestamp`, `managedFields`, `selfLink` — plus, on a create
  only, the fields an object that does not exist yet cannot have (`uid`,
  `resourceVersion`, `ownerReferences`, `status`). An update keeps all
  three of those on purpose: `resourceVersion` is the token the API
  *requires* for a PUT and is what turns a write that would clobber
  someone else's into a refusal; dropping `ownerReferences` would detach
  the object from its owner and defeat garbage collection; and a resource
  whose CRD declares no status subresource stores its status right there,
  where omitting it would wipe it.
- **Authorization needed nothing.** Requests carry the user's own token,
  so kcp authorizes each one in the project workspace against the RBAC a
  tenancy Membership materialized. A `view` member's delete is refused by
  the same authorizer that would refuse `kubectl` — the greyed-out button
  is a courtesy, the 403 is the control.

The table and the editor sit side by side: picking a row loads it into the
editor, so reading the list and changing something in it are one motion. A
create in progress is the one thing a row click will not interrupt — its
YAML was typed and exists nowhere else, whereas edits to an existing object
can be re-read from the server.

Namespaced resources are listed across namespaces and created in the one
named in the object, falling back to the namespace field on the page
(`default`, which is what the tenancy operator provisions in every project
workspace).

## Building

```sh
npm run build      # dist/
npm run typecheck
npm test           # the form-derivation spec
```

`dist/` is static: serve it from anywhere that can also reach the
front-proxy on the same origin.
