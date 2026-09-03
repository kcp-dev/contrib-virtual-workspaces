# contrib-virtual-workspaces

Contrib virtual workspaces for [kcp](https://github.com/kcp-dev/kcp).

| Component | What it serves | Image |
|---|---|---|
| [access/](access/) | **SelfClusterAccessReview** — "which workspaces can this user access?" in one call | `ghcr.io/kcp-dev/contrib-virtual-workspaces/access-vw` |
| [mcp/](mcp/) | [Model Context Protocol](https://modelcontextprotocol.io) as a virtual workspace, scoped to what the caller can see (uses the access VW) | `ghcr.io/kcp-dev/contrib-virtual-workspaces/mcp-vw` |
| [ephemeral/](ephemeral/) | Non-persisted resources: `POST` in, a provider webhook answers, nothing reaches etcd | `ghcr.io/kcp-dev/contrib-virtual-workspaces/ephemeral-vw` |

Each component's README documents its design, deployment and limits.

## Layout

- One `go.mod` at the root. The kcp Kubernetes fork replace block lives here
  once, and `hack/verify-fork-pin.sh` checks it against
  virtual-workspace-framework's.
- One `Dockerfile` with a shared builder stage and one final stage per
  component; images are selected with `--target access-vw|mcp-vw|ephemeral-vw`.
- The root `Makefile` owns building, testing and verification
  (`make build`, `make test`, `make verify`, `make images`,
  `make test-e2e-<component>`). Component Makefiles keep only the local
  development targets that need to run next to their manifests.
- CI runs one verify job, one build/test job, and one e2e job per component
  (`.github/workflows/ci.yaml`); images build as a three-way matrix over the
  Dockerfile targets (`.github/workflows/images.yaml`).

## Building

```sh
make build              # all binaries into bin/
make build-access       # or per component
make images             # all three container images
make image-mcp          # or per component
```

## Testing

```sh
make test               # unit tests
make verify             # gofmt, vet, fork pin, manifest rendering, tidy -diff
make test-e2e-access    # kind + kcp-operator
make test-e2e-mcp       # kind + kcp-operator + both VWs from this checkout
make test-e2e-ephemeral # local processes against a real kcp
```

## History

This repository merges three formerly separate repositories, imported with
full history:

- [contrib-access-virtual-workspace](https://github.com/kcp-dev/contrib-access-virtual-workspace) → `access/`
- [contrib-mcp-virtual-workspace](https://github.com/kcp-dev/contrib-mcp-virtual-workspace) → `mcp/`
- [contrib-virtual-ephemeral-resources-virtual-workspace](https://github.com/kcp-dev/contrib-virtual-ephemeral-resources-virtual-workspace) → `ephemeral/`
