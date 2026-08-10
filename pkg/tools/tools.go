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

// Package tools provides MCP tools for reading kcp API objects — workspaces,
// APIExports, APIBindings, APIResourceSchemas, WorkspaceTypes, LogicalClusters,
// Shards, Partitions, and scheduling resources — scoped to the workspaces the
// calling user can access.
//
// The package is intentionally self-contained so other MCP servers can reuse
// it: implement Scope and call Register on an mcp.Server.
package tools

import (
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
)

// Scope is the per-caller authorization context the tools operate within: the
// set of workspaces the caller may reach and how to obtain clients for them.
type Scope interface {
	// Names returns the workspace IDs the caller has access to.
	Names() []string

	// HasAccess reports whether workspace is in the caller's scope.
	HasAccess(workspace string) bool

	// ClientFor returns typed and dynamic clients for the given workspace,
	// acting as the caller.
	ClientFor(workspace string) (kubernetes.Interface, dynamic.Interface, error)

	// Clusters returns the raw workspace access info (name + endpoint).
	Clusters() []ClusterInfo
}

// ClusterInfo holds workspace endpoint information.
type ClusterInfo struct {
	ClusterName string
	Endpoint    string
}

// ScopeError is returned when a tool targets a workspace outside the caller's
// scope. It carries both the requested workspace and the available ones so the
// model understands the access boundary.
type ScopeError struct {
	Requested string   `json:"requested"`
	Available []string `json:"available"`
}

func (e *ScopeError) Error() string {
	return "workspace not in scope: " + e.Requested
}

// NewScopeError creates a ScopeError for the given workspace and scope.
func NewScopeError(workspace string, scope Scope) *ScopeError {
	return &ScopeError{
		Requested: workspace,
		Available: scope.Names(),
	}
}

// Register adds all kcp object tools to server, bound to scope.
func Register(server *mcp.Server, scope Scope) {
	registerListWorkspaces(server, scope)

	registerAPIExportTools(server, scope)
	registerAPIResourceSchemaTools(server, scope)
	registerAPIBindingTools(server, scope)

	registerTenancyTools(server, scope)
	registerWorkspaceStatusTools(server, scope)

	registerCoreTools(server, scope)

	registerReplicationTools(server, scope)

	registerWriteOperations(server, scope)
}

func extractListItems(list any) []map[string]any {
	if ul, ok := list.(interface{ UnstructuredContent() map[string]any }); ok {
		content := ul.UnstructuredContent()
		if items, ok := content["items"].([]any); ok {
			result := make([]map[string]any, 0, len(items))
			for _, item := range items {
				if m, ok := item.(map[string]any); ok {
					result = append(result, m)
				}
			}
			return result
		}
	}
	return nil
}

func extractObject(obj any) map[string]any {
	if u, ok := obj.(interface{ UnstructuredContent() map[string]any }); ok {
		return u.UnstructuredContent()
	}
	return nil
}
