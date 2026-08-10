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

package tools

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var (
	initializingWorkspaceGVR = schema.GroupVersionResource{
		Group:    "tenancy.kcp.io",
		Version:  "v1alpha1",
		Resource: "initializingworkspaces",
	}

	terminatingWorkspaceGVR = schema.GroupVersionResource{
		Group:    "tenancy.kcp.io",
		Version:  "v1alpha1",
		Resource: "terminatingworkspaces",
	}

	workspaceGVR = schema.GroupVersionResource{
		Group:    "tenancy.kcp.io",
		Version:  "v1alpha1",
		Resource: "workspaces",
	}
)

// WorkspaceStatusInfo represents workspace status in list output.
type WorkspaceStatusInfo struct {
	Name   string `json:"name"`
	Phase  string `json:"phase,omitempty"`
	Type   string `json:"type,omitempty"`
	URL    string `json:"url,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// ListWorkspacesStatusInput is the input for list_kcp_workspaces_status.
type ListWorkspacesStatusInput struct {
	Workspace string `json:"workspace"`
	Filter    string `json:"filter,omitempty"` // "all", "initializing", "terminating"
}

// ListWorkspacesStatusOutput is the output for list_kcp_workspaces_status.
type ListWorkspacesStatusOutput struct {
	Workspaces []WorkspaceStatusInfo `json:"workspaces"`
	Count      int                   `json:"count"`
}

// ListInitializingWorkspacesInput is the input for list_kcp_initializingworkspaces.
type ListInitializingWorkspacesInput struct {
	Workspace string `json:"workspace"`
}

// ListInitializingWorkspacesOutput is the output for list_kcp_initializingworkspaces.
type ListInitializingWorkspacesOutput struct {
	Workspaces []WorkspaceStatusInfo `json:"workspaces"`
	Count      int                   `json:"count"`
}

// ListTerminatingWorkspacesInput is the input for list_kcp_terminatingworkspaces.
type ListTerminatingWorkspacesInput struct {
	Workspace string `json:"workspace"`
}

// ListTerminatingWorkspacesOutput is the output for list_kcp_terminatingworkspaces.
type ListTerminatingWorkspacesOutput struct {
	Workspaces []WorkspaceStatusInfo `json:"workspaces"`
	Count      int                   `json:"count"`
}

func registerWorkspaceStatusTools(server *mcp.Server, scope Scope) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "list_kcp_initializingworkspaces",
		Description: `List InitializingWorkspaces in a kcp workspace.
InitializingWorkspaces are child workspaces that are still being set up.
They appear during workspace creation and disappear once initialization is complete.`,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"workspace": map[string]any{
					"type":        "string",
					"description": "Workspace ID (from list_kcp_workspaces)",
				},
			},
			"required": []string{"workspace"},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest, input ListInitializingWorkspacesInput) (*mcp.CallToolResult, ListInitializingWorkspacesOutput, error) {
		if !scope.HasAccess(input.Workspace) {
			return nil, ListInitializingWorkspacesOutput{}, NewScopeError(input.Workspace, scope)
		}

		_, dynClient, err := scope.ClientFor(input.Workspace)
		if err != nil {
			return nil, ListInitializingWorkspacesOutput{}, fmt.Errorf("getting client: %w", err)
		}

		list, err := dynClient.Resource(initializingWorkspaceGVR).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, ListInitializingWorkspacesOutput{}, fmt.Errorf("listing InitializingWorkspaces: %w", err)
		}

		items := extractListItems(list)
		workspaces := extractWorkspaceStatusInfos(items)

		return nil, ListInitializingWorkspacesOutput{
			Workspaces: workspaces,
			Count:      len(workspaces),
		}, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name: "list_kcp_terminatingworkspaces",
		Description: `List TerminatingWorkspaces in a kcp workspace.
TerminatingWorkspaces are child workspaces that are being deleted.
They appear during workspace deletion and disappear once termination is complete.`,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"workspace": map[string]any{
					"type":        "string",
					"description": "Workspace ID (from list_kcp_workspaces)",
				},
			},
			"required": []string{"workspace"},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest, input ListTerminatingWorkspacesInput) (*mcp.CallToolResult, ListTerminatingWorkspacesOutput, error) {
		if !scope.HasAccess(input.Workspace) {
			return nil, ListTerminatingWorkspacesOutput{}, NewScopeError(input.Workspace, scope)
		}

		_, dynClient, err := scope.ClientFor(input.Workspace)
		if err != nil {
			return nil, ListTerminatingWorkspacesOutput{}, fmt.Errorf("getting client: %w", err)
		}

		list, err := dynClient.Resource(terminatingWorkspaceGVR).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, ListTerminatingWorkspacesOutput{}, fmt.Errorf("listing TerminatingWorkspaces: %w", err)
		}

		items := extractListItems(list)
		workspaces := extractWorkspaceStatusInfos(items)

		return nil, ListTerminatingWorkspacesOutput{
			Workspaces: workspaces,
			Count:      len(workspaces),
		}, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name: "list_kcp_child_workspaces",
		Description: `List child Workspaces within a parent kcp workspace.
Returns all child workspaces visible from the given parent workspace.`,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"workspace": map[string]any{
					"type":        "string",
					"description": "Parent workspace ID (from list_kcp_workspaces)",
				},
			},
			"required": []string{"workspace"},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest, input ListWorkspacesStatusInput) (*mcp.CallToolResult, ListWorkspacesStatusOutput, error) {
		if !scope.HasAccess(input.Workspace) {
			return nil, ListWorkspacesStatusOutput{}, NewScopeError(input.Workspace, scope)
		}

		_, dynClient, err := scope.ClientFor(input.Workspace)
		if err != nil {
			return nil, ListWorkspacesStatusOutput{}, fmt.Errorf("getting client: %w", err)
		}

		list, err := dynClient.Resource(workspaceGVR).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, ListWorkspacesStatusOutput{}, fmt.Errorf("listing Workspaces: %w", err)
		}

		items := extractListItems(list)
		workspaces := extractWorkspaceStatusInfos(items)

		return nil, ListWorkspacesStatusOutput{
			Workspaces: workspaces,
			Count:      len(workspaces),
		}, nil
	})
}

// extractWorkspaceStatusInfos converts raw items to WorkspaceStatusInfo slice.
func extractWorkspaceStatusInfos(items []map[string]any) []WorkspaceStatusInfo {
	workspaces := make([]WorkspaceStatusInfo, 0, len(items))
	for _, item := range items {
		info := WorkspaceStatusInfo{}
		if meta, ok := item["metadata"].(map[string]any); ok {
			if name, ok := meta["name"].(string); ok {
				info.Name = name
			}
		}
		if spec, ok := item["spec"].(map[string]any); ok {
			if wsType, ok := spec["type"].(map[string]any); ok {
				if name, ok := wsType["name"].(string); ok {
					info.Type = name
				}
			}
		}
		if status, ok := item["status"].(map[string]any); ok {
			if phase, ok := status["phase"].(string); ok {
				info.Phase = phase
			}
			if url, ok := status["URL"].(string); ok {
				info.URL = url
			}
		}
		workspaces = append(workspaces, info)
	}
	return workspaces
}
