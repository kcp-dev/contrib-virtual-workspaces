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

// Package core is the default toolset: navigating the caller's workspaces
// (workspaces, APIBindings, APIExports, WorkspaceTypes, workspace lifecycle)
// plus generic read access to any resource by GVR. Everything is read-only;
// mutations live in the opt-in write toolset.
package core

import (
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/kcp-dev/contrib-virtual-workspaces/mcp/pkg/tools"
)

// Register adds the core toolset to server, bound to scope.
func Register(server *mcp.Server, scope tools.Scope) {
	registerListWorkspaces(server, scope)
	registerAPIBindingTools(server, scope)
	registerAPIExportTools(server, scope)
	registerAPIExportEndpointSliceTools(server, scope)
	registerTenancyTools(server, scope)
	registerWorkspaceStatusTools(server, scope)
	registerResourceTools(server, scope)
}
