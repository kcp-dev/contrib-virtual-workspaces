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

package apis

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/kcp-dev/contrib-mcp-virtual-workspace/pkg/tools"
)

var apiExportEndpointSliceGVR = schema.GroupVersionResource{
	Group:    "apis.kcp.io",
	Version:  "v1alpha1",
	Resource: "apiexportendpointslices",
}

// APIExportEndpointSliceInfo represents an APIExportEndpointSlice in list output.
type APIExportEndpointSliceInfo struct {
	Name     string `json:"name"`
	Export   string `json:"export,omitempty"`
	Endpoint string `json:"endpoint,omitempty"`
}

// ListAPIExportEndpointSlicesInput is the input for list_kcp_apiexportendpointslices.
type ListAPIExportEndpointSlicesInput struct {
	Workspace string `json:"workspace"`
}

// ListAPIExportEndpointSlicesOutput is the output for list_kcp_apiexportendpointslices.
type ListAPIExportEndpointSlicesOutput struct {
	Slices []APIExportEndpointSliceInfo `json:"slices"`
	Count  int                          `json:"count"`
}

func registerAPIExportEndpointSliceTools(server *mcp.Server, scope tools.Scope) {
	mcp.AddTool(server, &mcp.Tool{
		Annotations: tools.ReadOnly("List APIExportEndpointSlices"),
		Name:        "list_kcp_apiexportendpointslices",
		Description: `List APIExportEndpointSlices in a kcp workspace.
APIExportEndpointSlices contain the endpoints where an APIExport's virtual workspace can be accessed.`,
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
	}, func(ctx context.Context, req *mcp.CallToolRequest, input ListAPIExportEndpointSlicesInput) (*mcp.CallToolResult, ListAPIExportEndpointSlicesOutput, error) {
		if !scope.HasAccess(input.Workspace) {
			return nil, ListAPIExportEndpointSlicesOutput{}, tools.NewScopeError(input.Workspace, scope)
		}

		_, dynClient, err := scope.ClientFor(input.Workspace)
		if err != nil {
			return nil, ListAPIExportEndpointSlicesOutput{}, fmt.Errorf("getting client: %w", err)
		}

		list, err := dynClient.Resource(apiExportEndpointSliceGVR).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, ListAPIExportEndpointSlicesOutput{}, fmt.Errorf("listing APIExportEndpointSlices: %w", err)
		}

		items := tools.ExtractListItems(list)
		slices := make([]APIExportEndpointSliceInfo, 0, len(items))
		for _, item := range items {
			info := APIExportEndpointSliceInfo{}
			if meta, ok := item["metadata"].(map[string]any); ok {
				if name, ok := meta["name"].(string); ok {
					info.Name = name
				}
			}
			if spec, ok := item["spec"].(map[string]any); ok {
				if export, ok := spec["export"].(map[string]any); ok {
					if name, ok := export["name"].(string); ok {
						info.Export = name
					}
				}
			}
			if status, ok := item["status"].(map[string]any); ok {
				if endpoints, ok := status["endpoints"].([]any); ok && len(endpoints) > 0 {
					if ep, ok := endpoints[0].(map[string]any); ok {
						if url, ok := ep["url"].(string); ok {
							info.Endpoint = url
						}
					}
				}
			}
			slices = append(slices, info)
		}

		return nil, ListAPIExportEndpointSlicesOutput{
			Slices: slices,
			Count:  len(slices),
		}, nil
	})
}
