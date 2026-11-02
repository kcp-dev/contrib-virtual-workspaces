# contrib-virtual-workspaces

Virtual workspaces for [kcp](https://github.com/kcp-dev/kcp), and the
platform built out of two of them.

**kcp Platform** is [tenancy/](tenancy/) and [marketplace/](marketplace/)
together — tenants, projects and memberships, plus a catalog of offerings
to enable in them — served under the `*.platform.kcp.io` API groups. Its
web console lives in [kcp hub](https://github.com/olamilekan000/kcp-hub),
which runs these components as one platform. The other three components
are standalone virtual workspaces that stand on their own.

| Component | What it serves | Image |
|---|---|---|
| [access/](access/) | **SelfClusterAccessReview** — "which workspaces can this user access?" in one call | `ghcr.io/kcp-dev/contrib-virtual-workspaces/access-vw` |
| [mcp/](mcp/) | [Model Context Protocol](https://modelcontextprotocol.io) as a virtual workspace, scoped to what the caller can see (uses the access VW) | `ghcr.io/kcp-dev/contrib-virtual-workspaces/mcp-vw` |
| [ephemeral/](ephemeral/) | Non-persisted resources: `POST` in, a producer webhook answers, nothing reaches etcd | `ghcr.io/kcp-dev/contrib-virtual-workspaces/ephemeral-vw` |
| [tenancy/](tenancy/) | Tenants, projects and memberships as kcp workspaces and RBAC, plus **SelfTenancyReview** — "which tenants am I in?" — and the `tenancyctl` client | `ghcr.io/kcp-dev/contrib-virtual-workspaces/tenancy-vw` |
| [marketplace/](marketplace/) | Producers and offerings: a read-only catalog, where enabling is a plain kcp `APIBinding` the consumer authors themselves | `ghcr.io/kcp-dev/contrib-virtual-workspaces/marketplace-vw` |

Each component's README documents its design, deployment and limits.

## Layout

- One `go.mod` at the root. The kcp Kubernetes fork replace block lives here
  once, and `hack/verify-fork-pin.sh` checks it against
  virtual-workspace-framework's.
- One `Dockerfile` with a shared builder stage and one final stage per
  component; images are selected with `--target access-vw|mcp-vw|ephemeral-vw|tenancy-vw`.
- The root `Makefile` owns building, testing and verification
  (`make build`, `make test`, `make verify`, `make images`,
  `make test-e2e-<component>`). Component Makefiles keep only the local
  development targets that need to run next to their manifests.
- CI runs one verify job, one build/test job, and one e2e job per component
  (`.github/workflows/ci.yaml`); images build as a matrix over the
  Dockerfile targets (`.github/workflows/images.yaml`).

## Building

```sh
make build              # all binaries into bin/
make build-access       # or per component
make images             # all three container images
make image-mcp          # or per component
```

## One CLI

Every component that ships a client exposes it as importable cobra
commands, and `contribctl` (built by `make build-cli` into `bin/`)
arranges them around what a person does rather than around which
component implements it:

```sh
contribctl login --issuer https://dex.example.com --url https://kcp.example.com:8443
contribctl use                     # pick a tenant and project
contribctl tenants                 # what you can reach
contribctl offers                  # what you could enable
contribctl enable acme.things      # enable it here
contribctl membership add alice@example.com --role admin
```

The user-facing commands need no kubeconfig at all — the virtual
workspaces authenticate the caller and answer for the identity they hold.
`contribctl admin` is the other side, writing to kcp with a kubeconfig.
`login --url` remembers the server in `~/.kube/contribctl.json`, so
nothing after it has to be told where kcp is.

The standalone binaries (`tenancyctl`, `scar-to-kubeconfig`) remain the
same code under their own names.

## Building a producer

[producer-quickstart/](producer-quickstart/) is a producer ready to copy: a Go type, the
schema generated from it, a controller that reconciles it in every workspace
that enabled it, the instructions the console shows, a Dockerfile, a
deployment manifest and a CI workflow. Copy the directory, rename the
group, kind and producer, replace the fake provisioning in the reconciler,
and `make publish` puts it on the catalog. It is its own Go module so the
copy builds alone; `make verify` here builds and tests it so it cannot rot.

## Development with Tilt

`make tilt-up` (or `hack/tilt/up.sh`) brings up the whole stack in one
place: kcp itself comes from the kcp checkout's own Tilt setup
([contrib/tilt](https://github.com/kcp-dev/kcp/tree/main/contrib/tilt) —
operator, shards, front-proxy behind https://kcp.localhost:8443,
observability, dashboard), included unchanged by the repo
[Tiltfile](Tiltfile), and this repo's components run on top as
live-reloading local processes.

- `KCP_DIR` points at the kcp clone (default `../kcp`).
- Admin kubeconfigs land in `.tilt/` (`tilt-frontproxy.kubeconfig`, per
  shard), extracted by kcp's own tooling.
- Each component's virtual workspace is deployed **in the cluster** by the
  kcp-operator (a `VirtualWorkspace` object: Deployment, Service, serving
  certificate, and the component's own `init` as an init container) and
  routed by the front-proxy, so everything answers on one address —
  `https://kcp.localhost:8443`, kcp under `/clusters/` and each virtual
  workspace under `/services/<name>`. The operators run as local
  processes, which is where the fast iteration is.
- `TENANCY_ENABLED=false` / `MARKETPLACE_ENABLED=false` skip a component.
- Dex runs in the same kind cluster as the shared OIDC issuer
  (`https://dex.kcp.localhost:8443/dex`), trusted by kcp and both virtual
  workspaces, so a token names the same user everywhere. The environment
  is seeded with a demo tenant and project, so `whoami` has something to
  say right after login. `DEX_ENABLED=false` / `SEED_ENABLED=false` turn
  either off.

[hack/tilt/README.md](hack/tilt/README.md) walks the stack end to end:
bringing it up, logging in with Dex, and an ordinary user going from a
password to a working kubeconfig for exactly the workspaces they were
granted.

## Testing

An identity provider is shared by every component: [hack/dex/](hack/dex/)
runs Dex and writes the `AuthenticationConfiguration` that kcp and the
virtual workspaces both consume, so a username cannot drift between them.
The tenancy e2e uses it by default (`DEX=false` falls back to client
certificates where docker is unavailable).

```sh
make test               # unit tests
make verify             # gofmt, vet, fork pin, manifest rendering, tidy -diff
make test-e2e-access    # kind + kcp-operator
make test-e2e-mcp       # kind + kcp-operator + both VWs from this checkout
make test-e2e-ephemeral # local processes against a real kcp
make test-e2e-tenancy   # local processes against a real kcp
```

## History

This repository merges three formerly separate repositories, imported with
full history:

- [contrib-access-virtual-workspace](https://github.com/kcp-dev/contrib-access-virtual-workspace) → `access/`
- [contrib-mcp-virtual-workspace](https://github.com/kcp-dev/contrib-mcp-virtual-workspace) → `mcp/`
- [contrib-virtual-ephemeral-resources-virtual-workspace](https://github.com/kcp-dev/contrib-virtual-ephemeral-resources-virtual-workspace) → `ephemeral/`
