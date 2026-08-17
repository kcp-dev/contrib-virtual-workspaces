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

	"github.com/kcp-dev/contrib-mcp-virtual-workspace/pkg/tools"
)

// ListResourcesInput is the input for list_resources. The resource type is an
// explicit group+version+resource; group is empty for the core API group.
type ListResourcesInput struct {
	Workspace string `json:"workspace"`

	Group    string `json:"group,omitempty"`
	Version  string `json:"version"`
	Resource string `json:"resource"`

	Namespace     string `json:"namespace,omitempty"`
	LabelSelector string `json:"labelSelector,omitempty"`
	FieldSelector string `json:"fieldSelector,omitempty"`
}

// ListResourcesOutput is the output for list_resources.
type ListResourcesOutput struct {
	Items []map[string]any `json:"items"`
	Count int              `json:"count"`
}

// GetResourceInput is the input for get_resource. The resource type is an
// explicit group+version+resource; group is empty for the core API group.
type GetResourceInput struct {
	Workspace string `json:"workspace"`

	Group    string `json:"group,omitempty"`
	Version  string `json:"version"`
	Resource string `json:"resource"`

	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
}

// GetResourceOutput is the output for get_resource.
type GetResourceOutput struct {
	Object map[string]any `json:"object"`
}

func registerResourceTools(server *mcp.Server, scope tools.Scope) {
	mcp.AddTool(server, &mcp.Tool{
		Annotations: tools.ReadOnly("List resources"),
		Name:        "list_resources",
		Description: `List any Kubernetes resources in a workspace by type.

The type is group + version + plural resource name, e.g.:
- group "" (core), version "v1", resource "configmaps"
- group "apps", version "v1", resource "deployments"
- group "apis.kcp.io", version "v1alpha1", resource "apibindings"

Use this for any resource type without a dedicated tool, including CRDs and
APIs bound into the workspace. Use labelSelector (e.g. "app=nginx") and
fieldSelector (e.g. "metadata.name=foo") to filter. Omit namespace for
cluster-scoped resources or to list across all namespaces.`,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"workspace": map[string]any{
					"type":        "string",
					"description": "Workspace ID (from list_kcp_workspaces)",
				},
				"group": map[string]any{
					"type":        "string",
					"description": "API group (empty for the core group)",
				},
				"version": map[string]any{
					"type":        "string",
					"description": "API version (e.g. 'v1', 'v1alpha1')",
				},
				"resource": map[string]any{
					"type":        "string",
					"description": "Plural resource name (e.g. 'pods', 'deployments')",
				},
				"namespace": map[string]any{
					"type":        "string",
					"description": "Namespace (optional; omit for cluster-scoped or all namespaces)",
				},
				"labelSelector": map[string]any{
					"type":        "string",
					"description": "Label selector (e.g. 'app=nginx,env=prod')",
				},
				"fieldSelector": map[string]any{
					"type":        "string",
					"description": "Field selector (e.g. 'status.phase=Running')",
				},
			},
			"required": []string{"workspace", "version", "resource"},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest, input ListResourcesInput) (*mcp.CallToolResult, ListResourcesOutput, error) {
		if !scope.HasAccess(input.Workspace) {
			return nil, ListResourcesOutput{}, tools.NewScopeError(input.Workspace, scope)
		}

		_, dynClient, err := scope.ClientFor(input.Workspace)
		if err != nil {
			return nil, ListResourcesOutput{}, fmt.Errorf("getting client: %w", err)
		}

		gvr, err := tools.ParseGVR(input.Group, input.Version, input.Resource)
		if err != nil {
			return nil, ListResourcesOutput{}, err
		}

		listOpts := metav1.ListOptions{
			LabelSelector: input.LabelSelector,
			FieldSelector: input.FieldSelector,
		}

		var list any
		if input.Namespace != "" {
			list, err = dynClient.Resource(gvr).Namespace(input.Namespace).List(ctx, listOpts)
		} else {
			list, err = dynClient.Resource(gvr).List(ctx, listOpts)
		}
		if err != nil {
			return nil, ListResourcesOutput{}, fmt.Errorf("listing resources: %w", err)
		}

		items := tools.ExtractListItems(list)
		return nil, ListResourcesOutput{Items: items, Count: len(items)}, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Annotations: tools.ReadOnly("Get resource"),
		Name:        "get_resource",
		Description: `Get any Kubernetes resource by name from a workspace.

The type is group + version + plural resource name; see list_resources.`,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"workspace": map[string]any{
					"type":        "string",
					"description": "Workspace ID (from list_kcp_workspaces)",
				},
				"group": map[string]any{
					"type":        "string",
					"description": "API group (empty for the core group)",
				},
				"version": map[string]any{
					"type":        "string",
					"description": "API version (e.g. 'v1', 'v1alpha1')",
				},
				"resource": map[string]any{
					"type":        "string",
					"description": "Plural resource name (e.g. 'pods', 'deployments')",
				},
				"name": map[string]any{
					"type":        "string",
					"description": "Resource name",
				},
				"namespace": map[string]any{
					"type":        "string",
					"description": "Namespace (optional)",
				},
			},
			"required": []string{"workspace", "version", "resource", "name"},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest, input GetResourceInput) (*mcp.CallToolResult, GetResourceOutput, error) {
		if !scope.HasAccess(input.Workspace) {
			return nil, GetResourceOutput{}, tools.NewScopeError(input.Workspace, scope)
		}

		_, dynClient, err := scope.ClientFor(input.Workspace)
		if err != nil {
			return nil, GetResourceOutput{}, fmt.Errorf("getting client: %w", err)
		}

		gvr, err := tools.ParseGVR(input.Group, input.Version, input.Resource)
		if err != nil {
			return nil, GetResourceOutput{}, err
		}

		var obj any
		if input.Namespace != "" {
			obj, err = dynClient.Resource(gvr).Namespace(input.Namespace).Get(ctx, input.Name, metav1.GetOptions{})
		} else {
			obj, err = dynClient.Resource(gvr).Get(ctx, input.Name, metav1.GetOptions{})
		}
		if err != nil {
			return nil, GetResourceOutput{}, fmt.Errorf("getting resource: %w", err)
		}

		return nil, GetResourceOutput{Object: tools.ExtractObject(obj)}, nil
	})
}
