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

// Package apiexport holds the kcp-side assets the mcp component needs in
// the workspace hosting its APIExport.
//
// One export, and it is the whole enablement mechanism. MCP is not an API,
// so there is nothing for a consumer to bind in order to "get" it — what a
// consumer actually has to do is let the MCP server act as them in their
// workspace. This export is how: its permission claims cover exactly the
// RBAC the server needs to grant itself that, so binding it IS the
// consent, and deleting the binding revokes every claim it carried.
//
// They are embedded rather than applied with kubectl so the init binary
// can bootstrap a deployment on its own, following the same pattern as
// kcp's own config packages.
package apiexport

import "embed"

//go:embed *.yaml
var FS embed.FS

// Order is the sequence the assets must be applied in. It is load-bearing:
//
//   - the schema before the export, because an APIExport naming an
//     APIResourceSchema that does not exist yet is rejected;
//   - the bind RBAC before the endpoint slice, because kcp gates slice
//     creation on a `bind` verb against the export that cluster-admin does
//     not satisfy.
var Order = []string{
	"apiresourceschema-mcpendpoints.mcp.platform.kcp.io.yaml",
	"apiexport-mcp.yaml",
	"instructions.yaml",
	"rbac-bind.yaml",
	"apiexportendpointslice.yaml",
}
