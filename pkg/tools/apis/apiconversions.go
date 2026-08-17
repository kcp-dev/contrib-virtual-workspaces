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

var apiConversionGVR = schema.GroupVersionResource{
	Group:    "apis.kcp.io",
	Version:  "v1alpha1",
	Resource: "apiconversions",
}

// APIConversionInfo represents an APIConversion in list output.
type APIConversionInfo struct {
	Name string `json:"name"`
}

// ListAPIConversionsInput is the input for list_kcp_apiconversions.
type ListAPIConversionsInput struct {
	Workspace string `json:"workspace"`
}

// ListAPIConversionsOutput is the output for list_kcp_apiconversions.
type ListAPIConversionsOutput struct {
	APIConversions []APIConversionInfo `json:"apiConversions"`
	Count          int                 `json:"count"`
}

func registerAPIConversionTools(server *mcp.Server, scope tools.Scope) {
	mcp.AddTool(server, &mcp.Tool{
		Annotations: tools.ReadOnly("List APIConversions"),
		Name:        "list_kcp_apiconversions",
		Description: `List APIConversions in a kcp workspace.
APIConversions define how to convert between different versions of an API.`,
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
	}, func(ctx context.Context, req *mcp.CallToolRequest, input ListAPIConversionsInput) (*mcp.CallToolResult, ListAPIConversionsOutput, error) {
		if !scope.HasAccess(input.Workspace) {
			return nil, ListAPIConversionsOutput{}, tools.NewScopeError(input.Workspace, scope)
		}

		_, dynClient, err := scope.ClientFor(input.Workspace)
		if err != nil {
			return nil, ListAPIConversionsOutput{}, fmt.Errorf("getting client: %w", err)
		}

		list, err := dynClient.Resource(apiConversionGVR).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, ListAPIConversionsOutput{}, fmt.Errorf("listing APIConversions: %w", err)
		}

		items := tools.ExtractListItems(list)
		conversions := make([]APIConversionInfo, 0, len(items))
		for _, item := range items {
			info := APIConversionInfo{}
			if meta, ok := item["metadata"].(map[string]any); ok {
				if name, ok := meta["name"].(string); ok {
					info.Name = name
				}
			}
			conversions = append(conversions, info)
		}

		return nil, ListAPIConversionsOutput{
			APIConversions: conversions,
			Count:          len(conversions),
		}, nil
	})
}
