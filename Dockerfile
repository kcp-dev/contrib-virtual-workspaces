# syntax=docker/dockerfile:1.4

# Copyright 2026 The kcp Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# One Dockerfile, three images. Every component shares the builder below, so
# `go mod download` and the build cache are paid for once; the final stage is
# selected with --target:
#
#   docker build --target access-vw    -t access-vw .
#   docker build --target mcp-vw       -t mcp-vw .
#   docker build --target ephemeral-vw -t ephemeral-vw .

# Pinned to the build platform so multi-arch builds cross-compile instead of
# running the toolchain under emulation.
FROM --platform=$BUILDPLATFORM golang:1.26.4 AS builder
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
ENV CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH}

# ── access ───────────────────────────────────────────────────────────
FROM builder AS build-access
# The init and scar-to-kubeconfig binaries ship in the same image so a
# Deployment can run them as init containers / sidecars without pulling a
# second artifact.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go build -o /out/access-vw ./access/cmd/access-vw/ && \
    go build -o /out/access-vw-init ./access/cmd/init/ && \
    go build -o /out/scar-to-kubeconfig ./access/cmd/scar-to-kubeconfig/

FROM gcr.io/distroless/static:nonroot AS access-vw
COPY --from=build-access /out/ /
USER 65532:65532
ENTRYPOINT ["/access-vw"]

# ── mcp ──────────────────────────────────────────────────────────────
FROM builder AS build-mcp
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go build -o /out/mcp-virtual-workspace ./mcp/

FROM gcr.io/distroless/static:nonroot AS mcp-vw
COPY --from=build-mcp /out/ /
USER 65532:65532
ENTRYPOINT ["/mcp-virtual-workspace"]

# ── ephemeral ────────────────────────────────────────────────────────
FROM builder AS build-ephemeral
# endpointslice-controller is the other half of a deployment; example-webhook
# is the provider's side of the contract, shipped so the request path can be
# demonstrated from the image alone (the e2e tests and README rely on it).
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go build -o /out/ephemeral-virtual-workspace ./ephemeral/cmd/ephemeral-virtual-workspace/ && \
    go build -o /out/endpointslice-controller ./ephemeral/cmd/endpointslice-controller/ && \
    go build -o /out/example-webhook ./ephemeral/examples/webhook/

FROM gcr.io/distroless/static:nonroot AS ephemeral-vw
COPY --from=build-ephemeral /out/ /
USER 65532:65532
ENTRYPOINT ["/ephemeral-virtual-workspace"]
