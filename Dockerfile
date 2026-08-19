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

# Pinned to the build platform so multi-arch builds cross-compile instead of
# running the toolchain under emulation.
FROM --platform=$BUILDPLATFORM golang:1.26.0 AS builder
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
ENV CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH}
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go build -o /ephemeral-virtual-workspace ./cmd/ephemeral-virtual-workspace/
# The other half of a deployment: it publishes this server's address into the
# endpoint slice an APIExport references, and runs in the provider's workspace
# rather than alongside the server. Shipped here so that a deployment needs one
# artifact rather than two.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go build -o /endpointslice-controller ./cmd/endpointslice-controller/
# Not part of a deployment -- it is the provider's side of the contract, which
# a provider writes themselves. It is here so that the request path can be
# demonstrated from the image alone, which is what the e2e tests and the
# walkthrough in the README both need.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go build -o /example-webhook ./examples/webhook/

FROM gcr.io/distroless/static:nonroot
COPY --from=builder /ephemeral-virtual-workspace /ephemeral-virtual-workspace
COPY --from=builder /endpointslice-controller /endpointslice-controller
COPY --from=builder /example-webhook /example-webhook
USER 65532:65532
ENTRYPOINT ["/ephemeral-virtual-workspace"]
