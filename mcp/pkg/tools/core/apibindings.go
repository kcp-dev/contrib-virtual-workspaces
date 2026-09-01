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
	apiBindingGVR = schema.GroupVersionResource{
		Group:    "apis.kcp.io",
		Version:  "v1alpha2",
		Resource: "apibindings",
	}
)

// APIBindingInfo represents an APIBinding in list output.
type APIBindingInfo struct {
	Name       string `json:"name"`
	ExportName string `json:"exportName,omitempty"`
	ExportPath string `json:"exportPath,omitempty"`
	Phase      string `json:"phase,omitempty"`
}

// ListAPIBindingsInput is the input for list_kcp_apibindings.
type ListAPIBindingsInput struct {
	Workspace string `json:"workspace"`
}

// ListAPIBindingsOutput is the output for list_kcp_apibindings.
type ListAPIBindingsOutput struct {
	APIBindings []APIBindingInfo `json:"apiBindings"`
	Count       int              `json:"count"`
}

// GetAPIBindingInput is the input for get_kcp_apibinding.
type GetAPIBindingInput struct {
	Workspace string `json:"workspace"`
	Name      string `json:"name"`
}

// GetAPIBindingOutput is the output for get_kcp_apibinding.
type GetAPIBindingOutput struct {
	Object map[string]any `json:"object"`
}

func registerAPIBindingTools(server *mcp.Server, scope tools.Scope) {
	mcp.AddTool(server, &mcp.Tool{
		Annotations: tools.ReadOnly("List APIBindings"),
		Name:        "list_kcp_apibindings",
		Description: `List APIBindings in a kcp workspace.
APIBindings connect a workspace to APIs exported by another workspace via APIExport.
They are how consumers access shared APIs in kcp's multi-tenant model.`,
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
	}, func(ctx context.Context, req *mcp.CallToolRequest, input ListAPIBindingsInput) (*mcp.CallToolResult, ListAPIBindingsOutput, error) {
		if !scope.HasAccess(input.Workspace) {
			return nil, ListAPIBindingsOutput{}, tools.NewScopeError(input.Workspace, scope)
		}

		_, dynClient, err := scope.ClientFor(input.Workspace)
		if err != nil {
			return nil, ListAPIBindingsOutput{}, fmt.Errorf("getting client: %w", err)
		}

		list, err := dynClient.Resource(apiBindingGVR).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, ListAPIBindingsOutput{}, fmt.Errorf("listing APIBindings: %w", err)
		}

		items := tools.ExtractListItems(list)
		bindings := make([]APIBindingInfo, 0, len(items))
		for _, item := range items {
			info := APIBindingInfo{}
			if meta, ok := item["metadata"].(map[string]any); ok {
				if name, ok := meta["name"].(string); ok {
					info.Name = name
				}
			}
			if spec, ok := item["spec"].(map[string]any); ok {
				if ref, ok := spec["reference"].(map[string]any); ok {
					if export, ok := ref["export"].(map[string]any); ok {
						if name, ok := export["name"].(string); ok {
							info.ExportName = name
						}
						if path, ok := export["path"].(string); ok {
							info.ExportPath = path
						}
					}
				}
			}
			if status, ok := item["status"].(map[string]any); ok {
				if phase, ok := status["phase"].(string); ok {
					info.Phase = phase
				}
			}
			bindings = append(bindings, info)
		}

		return nil, ListAPIBindingsOutput{
			APIBindings: bindings,
			Count:       len(bindings),
		}, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Annotations: tools.ReadOnly("Get APIBinding"),
		Name:        "get_kcp_apibinding",
		Description: `Get a specific APIBinding by name from a workspace.`,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"workspace": map[string]any{
					"type":        "string",
					"description": "Workspace ID",
				},
				"name": map[string]any{
					"type":        "string",
					"description": "APIBinding name",
				},
			},
			"required": []string{"workspace", "name"},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest, input GetAPIBindingInput) (*mcp.CallToolResult, GetAPIBindingOutput, error) {
		if !scope.HasAccess(input.Workspace) {
			return nil, GetAPIBindingOutput{}, tools.NewScopeError(input.Workspace, scope)
		}

		_, dynClient, err := scope.ClientFor(input.Workspace)
		if err != nil {
			return nil, GetAPIBindingOutput{}, fmt.Errorf("getting client: %w", err)
		}

		obj, err := dynClient.Resource(apiBindingGVR).Get(ctx, input.Name, metav1.GetOptions{})
		if err != nil {
			return nil, GetAPIBindingOutput{}, fmt.Errorf("getting APIBinding: %w", err)
		}

		return nil, GetAPIBindingOutput{Object: tools.ExtractObject(obj)}, nil
	})
}
