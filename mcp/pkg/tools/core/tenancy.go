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
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/kcp-dev/contrib-virtual-workspaces/mcp/pkg/tools"
)

var (
	workspaceTypeGVR = schema.GroupVersionResource{
		Group:    "tenancy.kcp.io",
		Version:  "v1alpha1",
		Resource: "workspacetypes",
	}
)

// WorkspaceTypeInfo represents a WorkspaceType in list output.
type WorkspaceTypeInfo struct {
	Name           string   `json:"name"`
	Description    string   `json:"description,omitempty"`
	Initializers   []string `json:"initializers,omitempty"`
	DefaultAPIPath string   `json:"defaultAPIPath,omitempty"`
}

// ListWorkspaceTypesInput is the input for list_kcp_workspacetypes.
type ListWorkspaceTypesInput struct {
	Workspace string `json:"workspace"`
}

// ListWorkspaceTypesOutput is the output for list_kcp_workspacetypes.
type ListWorkspaceTypesOutput struct {
	WorkspaceTypes []WorkspaceTypeInfo `json:"workspaceTypes"`
	Count          int                 `json:"count"`
}

// GetWorkspaceTypeInput is the input for get_kcp_workspacetype.
type GetWorkspaceTypeInput struct {
	Workspace string `json:"workspace"`
	Name      string `json:"name"`
}

// GetWorkspaceTypeOutput is the output for get_kcp_workspacetype.
type GetWorkspaceTypeOutput struct {
	Object map[string]any `json:"object"`
}

func registerTenancyTools(server *mcp.Server, scope tools.Scope) {
	mcp.AddTool(server, &mcp.Tool{
		Annotations: tools.ReadOnly("List WorkspaceTypes"),
		Name:        "list_kcp_workspacetypes",
		Description: `List WorkspaceTypes in a kcp workspace.
WorkspaceTypes define templates for creating new workspaces, including:
- Which initializers run when a workspace is created
- Default API bindings
- Allowed child workspace types`,
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
	}, func(ctx context.Context, req *mcp.CallToolRequest, input ListWorkspaceTypesInput) (*mcp.CallToolResult, ListWorkspaceTypesOutput, error) {
		if !scope.HasAccess(input.Workspace) {
			return nil, ListWorkspaceTypesOutput{}, tools.NewScopeError(input.Workspace, scope)
		}

		_, dynClient, err := scope.ClientFor(input.Workspace)
		if err != nil {
			return nil, ListWorkspaceTypesOutput{}, fmt.Errorf("getting client: %w", err)
		}

		list, err := dynClient.Resource(workspaceTypeGVR).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, ListWorkspaceTypesOutput{}, fmt.Errorf("listing WorkspaceTypes: %w", err)
		}

		items := tools.ExtractListItems(list)
		types := make([]WorkspaceTypeInfo, 0, len(items))
		for _, item := range items {
			info := WorkspaceTypeInfo{}
			if meta, ok := item["metadata"].(map[string]any); ok {
				if name, ok := meta["name"].(string); ok {
					info.Name = name
				}
			}
			if spec, ok := item["spec"].(map[string]any); ok {
				if desc, ok := spec["description"].(string); ok {
					info.Description = desc
				}
				if inits, ok := spec["initializers"].([]any); ok {
					info.Initializers = make([]string, 0, len(inits))
					for _, init := range inits {
						if s, ok := init.(string); ok {
							info.Initializers = append(info.Initializers, s)
						}
					}
				}
			}
			types = append(types, info)
		}

		return nil, ListWorkspaceTypesOutput{
			WorkspaceTypes: types,
			Count:          len(types),
		}, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Annotations: tools.ReadOnly("Get WorkspaceType"),
		Name:        "get_kcp_workspacetype",
		Description: `Get a specific WorkspaceType by name from a workspace.`,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"workspace": map[string]any{
					"type":        "string",
					"description": "Workspace ID",
				},
				"name": map[string]any{
					"type":        "string",
					"description": "WorkspaceType name",
				},
			},
			"required": []string{"workspace", "name"},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest, input GetWorkspaceTypeInput) (*mcp.CallToolResult, GetWorkspaceTypeOutput, error) {
		if !scope.HasAccess(input.Workspace) {
			return nil, GetWorkspaceTypeOutput{}, tools.NewScopeError(input.Workspace, scope)
		}

		_, dynClient, err := scope.ClientFor(input.Workspace)
		if err != nil {
			return nil, GetWorkspaceTypeOutput{}, fmt.Errorf("getting client: %w", err)
		}

		obj, err := dynClient.Resource(workspaceTypeGVR).Get(ctx, input.Name, metav1.GetOptions{})
		if err != nil {
			return nil, GetWorkspaceTypeOutput{}, fmt.Errorf("getting WorkspaceType: %w", err)
		}

		return nil, GetWorkspaceTypeOutput{Object: tools.ExtractObject(obj)}, nil
	})
}
