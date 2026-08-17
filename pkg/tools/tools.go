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

// Package tools is the shared kit for kcp MCP tools: the Scope authorization
// contract every tool operates within, plus helpers for GVR parsing and
// unstructured object handling.
package tools

import (
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
)

// ReadOnly returns tool annotations with a human-readable title marking a
// tool as read-only, so MCP clients can distinguish safe reads from mutations
// (e.g. for auto-approval).
func ReadOnly(title string) *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{Title: title, ReadOnlyHint: true}
}

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

// ExtractListItems returns the items of an unstructured list as raw maps.
func ExtractListItems(list any) []map[string]any {
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

// ExtractObject returns an unstructured object's content as a raw map.
func ExtractObject(obj any) map[string]any {
	if u, ok := obj.(interface{ UnstructuredContent() map[string]any }); ok {
		return u.UnstructuredContent()
	}
	return nil
}
