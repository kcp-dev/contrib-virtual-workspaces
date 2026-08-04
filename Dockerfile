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

FROM golang:1.26.4 AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /access-vw ./cmd/access-vw/
# Shipped in the same image so a Deployment can run it as an init container
# without pulling a second artifact.
RUN CGO_ENABLED=0 GOOS=linux go build -o /access-vw-init ./cmd/init/
RUN CGO_ENABLED=0 GOOS=linux go build -o /scar-to-kubeconfig ./cmd/scar-to-kubeconfig/

FROM gcr.io/distroless/static:nonroot
COPY --from=builder /access-vw /access-vw
COPY --from=builder /access-vw-init /access-vw-init
COPY --from=builder /scar-to-kubeconfig /scar-to-kubeconfig
USER 65532:65532
ENTRYPOINT ["/access-vw"]
