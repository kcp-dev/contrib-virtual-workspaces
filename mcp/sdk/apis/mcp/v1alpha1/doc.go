/*
Copyright 2026 The kcp Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package v1alpha1 holds the API type for the mcp component (group
// mcp.platform.kcp.io).
//
// One kind, MCPEndpoint, and it is a report rather than a request: the
// operator writes it into a workspace that has enabled MCP, and whoever
// works there reads it to find out where to point a client and whether
// the enablement finished. Enabling is the APIBinding, not this object —
// which is why there is nothing here to create and no spec to fill in.
//
// +k8s:openapi-gen=true
// +groupName=mcp.platform.kcp.io
package v1alpha1
