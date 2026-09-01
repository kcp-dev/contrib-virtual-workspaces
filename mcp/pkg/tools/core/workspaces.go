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

package core

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/kcp-dev/contrib-virtual-workspaces/mcp/pkg/tools"
)

// WorkspaceInfo represents a single workspace in the list_kcp_workspaces output.
type WorkspaceInfo struct {
	ID       string `json:"id"`
	Endpoint string `json:"endpoint"`
}

// ListWorkspacesInput is the input schema for list_kcp_workspaces (empty).
type ListWorkspacesInput struct{}

// ListWorkspacesOutput is the output schema for list_kcp_workspaces.
type ListWorkspacesOutput struct {
	Workspaces []WorkspaceInfo `json:"workspaces"`
}

func registerListWorkspaces(server *mcp.Server, scope tools.Scope) {
	mcp.AddTool(server, &mcp.Tool{
		Annotations: tools.ReadOnly("List workspaces"),
		Name:        "list_kcp_workspaces",
		Description: "List kcp workspaces the authenticated user has access to. Returns workspace IDs and their API endpoints.",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest, input ListWorkspacesInput) (*mcp.CallToolResult, ListWorkspacesOutput, error) {
		clusters := scope.Clusters()
		workspaces := make([]WorkspaceInfo, len(clusters))
		for i, c := range clusters {
			workspaces[i] = WorkspaceInfo{
				ID:       c.ClusterName,
				Endpoint: c.Endpoint,
			}
		}
		return nil, ListWorkspacesOutput{Workspaces: workspaces}, nil
	})
}
