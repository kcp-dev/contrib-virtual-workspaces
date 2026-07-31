# syntax=docker/dockerfile:1.4

FROM golang:1.26.4 AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /mcp-virtual-workspace .

FROM gcr.io/distroless/static:nonroot
COPY --from=builder /mcp-virtual-workspace /mcp-virtual-workspace
USER 65532:65532
ENTRYPOINT ["/mcp-virtual-workspace"]
