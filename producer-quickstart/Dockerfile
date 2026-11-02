# syntax=docker/dockerfile:1.4
# Pinned to the build platform so multi-arch builds cross-compile instead of
# running the toolchain under emulation.
FROM --platform=$BUILDPLATFORM golang:1.26 AS builder
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
ENV CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH}
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go build -o /out/producer ./cmd/producer/

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=builder /out/producer /producer
USER 65532:65532
ENTRYPOINT ["/producer"]
