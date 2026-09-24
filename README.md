# contrib-virtual-workspaces

Contrib virtual workspaces for [kcp](https://github.com/kcp-dev/kcp).

| Component | What it serves | Image |
|---|---|---|
| [access/](access/) | **SelfClusterAccessReview** — "which workspaces can this user access?" in one call | `ghcr.io/kcp-dev/contrib-virtual-workspaces/access-vw` |
| [mcp/](mcp/) | [Model Context Protocol](https://modelcontextprotocol.io) as a virtual workspace, scoped to what the caller can see (uses the access VW) | `ghcr.io/kcp-dev/contrib-virtual-workspaces/mcp-vw` |
| [ephemeral/](ephemeral/) | Non-persisted resources: `POST` in, a provider webhook answers, nothing reaches etcd | `ghcr.io/kcp-dev/contrib-virtual-workspaces/ephemeral-vw` |
| [tenancy/](tenancy/) | Tenants, projects and memberships as kcp workspaces and RBAC, plus **SelfTenancyReview** — "which tenants am I in?" — and the `tenancyctl` client | `ghcr.io/kcp-dev/contrib-virtual-workspaces/tenancy-vw` |

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

Every component that ships a client exposes it as an importable cobra
command, and `contribctl` (built by `make build-cli` into `bin/`) mounts
them all under one root:

```sh
contribctl tenancy tenant create acme --display-name "Acme Corp"   # tenancyctl
contribctl scar-to-kubeconfig --token "$TOKEN"                     # access helper
```

The standalone binaries (`tenancyctl`, `scar-to-kubeconfig`) remain the
same code under their own names. A new component adds one
`AddCommand(...)` line in [cmd/cli/main.go](cmd/cli/main.go).

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
- Per component you get build → init → operator → virtual workspace
  resources; a source change rebuilds the binary, re-runs `init` (the
  kcp-side assets are embedded, so a changed asset must be re-applied)
  and restarts both processes. The tenancy VW serves on :9445, the
  marketplace VW on :9446, with a dev client CA in `.tilt/pki`.
- `TENANCY_ENABLED=false` / `MARKETPLACE_ENABLED=false` skip a component.
- Dex runs in the same kind cluster as the shared OIDC issuer
  (`https://dex.kcp.localhost:8443/dex`, SNI-routed through the same
  envoy gateway and `:8443` port-forward; add `dex.kcp.localhost` to the
  `127.0.0.1` line in `/etc/hosts`). kcp and both virtual workspaces
  validate its JWTs with the same mapping — `usernameClaim: email`, no
  prefix — so a token names the same user everywhere.
  `DEX_ENABLED=false` turns it off.

  The users (all with password `password`; log in with the full email):

  | login (email) | kcp/VW username |
  |---|---|
  | `alice@team-a.example.com` | `alice@team-a.example.com` |
  | `bob@team-b.example.com` | `bob@team-b.example.com` |
  | `mallory@strangers.example.com` | `mallory@strangers.example.com` |

  ```sh
  # browser flow: dex shows a login page, enter the email + "password"
  bin/contribctl tenancy login \
    --issuer https://dex.kcp.localhost:8443/dex --ca-file .tilt/dex/ca.crt

  # scripted: password grant, no browser
  echo password | bin/contribctl tenancy login \
    --issuer https://dex.kcp.localhost:8443/dex --ca-file .tilt/dex/ca.crt \
    --username alice@team-a.example.com --password-stdin
  ```

  The environment is seeded with demo tenancy data
  ([hack/tilt/seed.sh](hack/tilt/seed.sh), idempotent — re-run with
  `tilt trigger tenancy-seed`, skip with `SEED_ENABLED=false`): tenant
  `acme` with project `web`; alice is admin of the tenant and bob has
  view on the project, each under both identities (dex email and
  dev-cert CN). So right after login, `contribctl tenancy whoami` has
  something to say.

  Unlike the manual [hack/dex](hack/dex/) walkthrough (which maps
  `alice@team-a.example.com` → `alice`/`team-a` with CEL in an
  `AuthenticationConfiguration`), the kcp-operator only speaks the legacy
  `--oidc-*` claim mapping: the identity under Tilt is the **plain
  email**, with no groups. Memberships and RBAC here must name
  `alice@team-a.example.com` — while the `.tilt/pki` client certificates
  still authenticate at the VWs as `alice`/`bob`.

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
