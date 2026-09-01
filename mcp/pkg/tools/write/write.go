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

// Package write provides generic mutation tools — create, update, patch,
// delete and scale by GVR. It is opt-in: mutations through free-form
// manifests are for operators who accept that risk, not the default surface.
package write

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/yaml"

	"github.com/kcp-dev/contrib-virtual-workspaces/mcp/pkg/tools"
)

// CreateResourceInput is the input for create_resource.
type CreateResourceInput struct {
	Workspace string `json:"workspace"`
	Resource  string `json:"resource"`
}

// CreateResourceOutput is the output for create_resource.
type CreateResourceOutput struct {
	Created map[string]any `json:"created"`
	Message string         `json:"message"`
}

// UpdateResourceInput is the input for update_resource.
type UpdateResourceInput struct {
	Workspace string `json:"workspace"`
	Resource  string `json:"resource"`
}

// UpdateResourceOutput is the output for update_resource.
type UpdateResourceOutput struct {
	Updated map[string]any `json:"updated"`
	Message string         `json:"message"`
}

// PatchResourceInput is the input for patch_resource.
type PatchResourceInput struct {
	Workspace string `json:"workspace"`

	Group    string `json:"group,omitempty"`
	Version  string `json:"version"`
	Resource string `json:"resource"`

	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`

	Patch string `json:"patch"`
}

// PatchResourceOutput is the output for patch_resource.
type PatchResourceOutput struct {
	Patched map[string]any `json:"patched"`
	Message string         `json:"message"`
}

// DeleteResourceInput is the input for delete_resource.
type DeleteResourceInput struct {
	Workspace string `json:"workspace"`

	Group    string `json:"group,omitempty"`
	Version  string `json:"version"`
	Resource string `json:"resource"`

	Name               string `json:"name"`
	Namespace          string `json:"namespace,omitempty"`
	GracePeriodSeconds *int64 `json:"gracePeriodSeconds,omitempty"`
}

// DeleteResourceOutput is the output for delete_resource.
type DeleteResourceOutput struct {
	Message string `json:"message"`
}

// ScaleResourceInput is the input for scale_resource.
type ScaleResourceInput struct {
	Workspace string `json:"workspace"`

	Group    string `json:"group,omitempty"`
	Version  string `json:"version"`
	Resource string `json:"resource"`

	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
	Replicas  *int32 `json:"replicas,omitempty"`
}

// ScaleResourceOutput is the output for scale_resource.
type ScaleResourceOutput struct {
	Name            string `json:"name"`
	Namespace       string `json:"namespace,omitempty"`
	CurrentReplicas int32  `json:"currentReplicas"`
	DesiredReplicas int32  `json:"desiredReplicas"`
	Message         string `json:"message"`
}

// Register adds the write toolset to server, bound to scope.
func Register(server *mcp.Server, scope tools.Scope) {
	registerCreateResource(server, scope)
	registerUpdateResource(server, scope)
	registerPatchResource(server, scope)
	registerDeleteResource(server, scope)
	registerScaleResource(server, scope)
}

func registerCreateResource(server *mcp.Server, scope tools.Scope) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "create_resource",
		Annotations: &mcp.ToolAnnotations{
			Title:           "Create resource",
			DestructiveHint: new(false),
		},
		Description: `Create a Kubernetes resource in a workspace.

Provide the resource as YAML or JSON with apiVersion, kind, metadata, and spec.

Example YAML:
  apiVersion: v1
  kind: ConfigMap
  metadata:
    name: my-config
    namespace: default
  data:
    key: value

The resource will be created in the specified workspace.`,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"workspace": map[string]any{
					"type":        "string",
					"description": "Workspace ID (from list_kcp_workspaces)",
				},
				"resource": map[string]any{
					"type":        "string",
					"description": "YAML or JSON representation of the Kubernetes resource",
				},
			},
			"required": []string{"workspace", "resource"},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest, input CreateResourceInput) (*mcp.CallToolResult, CreateResourceOutput, error) {
		if !scope.HasAccess(input.Workspace) {
			return nil, CreateResourceOutput{}, tools.NewScopeError(input.Workspace, scope)
		}

		kubeClient, dynClient, err := scope.ClientFor(input.Workspace)
		if err != nil {
			return nil, CreateResourceOutput{}, fmt.Errorf("getting client: %w", err)
		}

		obj, err := parseManifest(input.Resource)
		if err != nil {
			return nil, CreateResourceOutput{}, fmt.Errorf("parsing resource: %w", err)
		}

		gvr, err := tools.ResolveGVR(kubeClient.Discovery(), obj.GetAPIVersion(), obj.GetKind())
		if err != nil {
			return nil, CreateResourceOutput{}, err
		}

		var created *unstructured.Unstructured
		ns := obj.GetNamespace()
		if ns != "" {
			created, err = dynClient.Resource(gvr).Namespace(ns).Create(ctx, obj, metav1.CreateOptions{})
		} else {
			created, err = dynClient.Resource(gvr).Create(ctx, obj, metav1.CreateOptions{})
		}
		if err != nil {
			return nil, CreateResourceOutput{}, fmt.Errorf("creating resource: %w", err)
		}

		return nil, CreateResourceOutput{
			Created: created.UnstructuredContent(),
			Message: fmt.Sprintf("Created %s/%s", created.GetKind(), created.GetName()),
		}, nil
	})
}

func registerUpdateResource(server *mcp.Server, scope tools.Scope) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "update_resource",
		Annotations: &mcp.ToolAnnotations{
			Title:           "Update resource",
			DestructiveHint: new(true),
		},
		Description: `Update (replace) a Kubernetes resource in a workspace.

Provide the full resource as YAML or JSON. The resource must include:
- apiVersion, kind, metadata.name
- metadata.resourceVersion (get current version with get_resource first)

This performs a full replacement of the resource spec.
For partial updates, use patch_resource instead.`,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"workspace": map[string]any{
					"type":        "string",
					"description": "Workspace ID (from list_kcp_workspaces)",
				},
				"resource": map[string]any{
					"type":        "string",
					"description": "Full YAML or JSON representation of the resource (must include resourceVersion)",
				},
			},
			"required": []string{"workspace", "resource"},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest, input UpdateResourceInput) (*mcp.CallToolResult, UpdateResourceOutput, error) {
		if !scope.HasAccess(input.Workspace) {
			return nil, UpdateResourceOutput{}, tools.NewScopeError(input.Workspace, scope)
		}

		kubeClient, dynClient, err := scope.ClientFor(input.Workspace)
		if err != nil {
			return nil, UpdateResourceOutput{}, fmt.Errorf("getting client: %w", err)
		}

		obj, err := parseManifest(input.Resource)
		if err != nil {
			return nil, UpdateResourceOutput{}, fmt.Errorf("parsing resource: %w", err)
		}

		gvr, err := tools.ResolveGVR(kubeClient.Discovery(), obj.GetAPIVersion(), obj.GetKind())
		if err != nil {
			return nil, UpdateResourceOutput{}, err
		}

		var updated *unstructured.Unstructured
		ns := obj.GetNamespace()
		if ns != "" {
			updated, err = dynClient.Resource(gvr).Namespace(ns).Update(ctx, obj, metav1.UpdateOptions{})
		} else {
			updated, err = dynClient.Resource(gvr).Update(ctx, obj, metav1.UpdateOptions{})
		}
		if err != nil {
			return nil, UpdateResourceOutput{}, fmt.Errorf("updating resource: %w", err)
		}

		return nil, UpdateResourceOutput{
			Updated: updated.UnstructuredContent(),
			Message: fmt.Sprintf("Updated %s/%s", updated.GetKind(), updated.GetName()),
		}, nil
	})
}

func registerPatchResource(server *mcp.Server, scope tools.Scope) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "patch_resource",
		Annotations: &mcp.ToolAnnotations{
			Title:           "Patch resource",
			DestructiveHint: new(true),
			IdempotentHint:  true,
		},
		Description: `Patch a Kubernetes resource with a JSON merge patch.

Use this for partial updates without needing the full resource.
The patch is a JSON object that will be merged with the existing resource.

Example patch to add a label:
  {"metadata": {"labels": {"env": "prod"}}}

Example patch to update a ConfigMap data field:
  {"data": {"newKey": "newValue"}}`,
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
					"description": "Plural resource name (e.g. 'configmaps', 'deployments')",
				},
				"name": map[string]any{
					"type":        "string",
					"description": "Resource name",
				},
				"namespace": map[string]any{
					"type":        "string",
					"description": "Namespace (optional for cluster-scoped resources)",
				},
				"patch": map[string]any{
					"type":        "string",
					"description": "JSON merge patch content",
				},
			},
			"required": []string{"workspace", "version", "resource", "name", "patch"},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest, input PatchResourceInput) (*mcp.CallToolResult, PatchResourceOutput, error) {
		if !scope.HasAccess(input.Workspace) {
			return nil, PatchResourceOutput{}, tools.NewScopeError(input.Workspace, scope)
		}

		_, dynClient, err := scope.ClientFor(input.Workspace)
		if err != nil {
			return nil, PatchResourceOutput{}, fmt.Errorf("getting client: %w", err)
		}

		gvr, err := tools.ParseGVR(input.Group, input.Version, input.Resource)
		if err != nil {
			return nil, PatchResourceOutput{}, err
		}

		var patchData map[string]any
		if err := json.Unmarshal([]byte(input.Patch), &patchData); err != nil {
			return nil, PatchResourceOutput{}, fmt.Errorf("invalid patch JSON: %w", err)
		}

		var patched *unstructured.Unstructured
		if input.Namespace != "" {
			patched, err = dynClient.Resource(gvr).Namespace(input.Namespace).Patch(
				ctx, input.Name, types.MergePatchType, []byte(input.Patch), metav1.PatchOptions{})
		} else {
			patched, err = dynClient.Resource(gvr).Patch(
				ctx, input.Name, types.MergePatchType, []byte(input.Patch), metav1.PatchOptions{})
		}
		if err != nil {
			return nil, PatchResourceOutput{}, fmt.Errorf("patching resource: %w", err)
		}

		return nil, PatchResourceOutput{
			Patched: patched.UnstructuredContent(),
			Message: fmt.Sprintf("Patched %s/%s", patched.GetKind(), patched.GetName()),
		}, nil
	})
}

func registerDeleteResource(server *mcp.Server, scope tools.Scope) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "delete_resource",
		Annotations: &mcp.ToolAnnotations{
			Title:           "Delete resource",
			DestructiveHint: new(true),
		},
		Description: `Delete a Kubernetes resource from a workspace.

Specify the resource type as group+version+resource, plus name.
Optionally set gracePeriodSeconds for controlled termination (0 = immediate).`,
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
					"description": "Plural resource name (e.g. 'configmaps', 'deployments')",
				},
				"name": map[string]any{
					"type":        "string",
					"description": "Resource name to delete",
				},
				"namespace": map[string]any{
					"type":        "string",
					"description": "Namespace (optional for cluster-scoped resources)",
				},
				"gracePeriodSeconds": map[string]any{
					"type":        "integer",
					"description": "Seconds before deletion (0 = immediate, omit for default)",
				},
			},
			"required": []string{"workspace", "version", "resource", "name"},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest, input DeleteResourceInput) (*mcp.CallToolResult, DeleteResourceOutput, error) {
		if !scope.HasAccess(input.Workspace) {
			return nil, DeleteResourceOutput{}, tools.NewScopeError(input.Workspace, scope)
		}

		_, dynClient, err := scope.ClientFor(input.Workspace)
		if err != nil {
			return nil, DeleteResourceOutput{}, fmt.Errorf("getting client: %w", err)
		}

		gvr, err := tools.ParseGVR(input.Group, input.Version, input.Resource)
		if err != nil {
			return nil, DeleteResourceOutput{}, err
		}

		deleteOpts := metav1.DeleteOptions{}
		if input.GracePeriodSeconds != nil {
			deleteOpts.GracePeriodSeconds = input.GracePeriodSeconds
		}

		if input.Namespace != "" {
			err = dynClient.Resource(gvr).Namespace(input.Namespace).Delete(ctx, input.Name, deleteOpts)
		} else {
			err = dynClient.Resource(gvr).Delete(ctx, input.Name, deleteOpts)
		}
		if err != nil {
			return nil, DeleteResourceOutput{}, fmt.Errorf("deleting resource: %w", err)
		}

		return nil, DeleteResourceOutput{
			Message: fmt.Sprintf("Deleted %s %s", gvr.Resource, input.Name),
		}, nil
	})
}

func registerScaleResource(server *mcp.Server, scope tools.Scope) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "scale_resource",
		Annotations: &mcp.ToolAnnotations{
			Title:           "Scale resource",
			DestructiveHint: new(false),
			IdempotentHint:  true,
		},
		Description: `Get or set the scale (replicas) of a Kubernetes resource.

Works with Deployments, StatefulSets, ReplicaSets, and other scalable resources.
If replicas is omitted, returns the current scale without changing it.
If replicas is provided, scales the resource to that number.`,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"workspace": map[string]any{
					"type":        "string",
					"description": "Workspace ID (from list_kcp_workspaces)",
				},
				"group": map[string]any{
					"type":        "string",
					"description": "API group (e.g. 'apps')",
				},
				"version": map[string]any{
					"type":        "string",
					"description": "API version (e.g. 'v1')",
				},
				"resource": map[string]any{
					"type":        "string",
					"description": "Plural resource name (e.g. 'deployments', 'statefulsets')",
				},
				"name": map[string]any{
					"type":        "string",
					"description": "Resource name",
				},
				"namespace": map[string]any{
					"type":        "string",
					"description": "Namespace",
				},
				"replicas": map[string]any{
					"type":        "integer",
					"description": "Desired replica count (omit to just get current scale)",
				},
			},
			"required": []string{"workspace", "version", "resource", "name"},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest, input ScaleResourceInput) (*mcp.CallToolResult, ScaleResourceOutput, error) {
		if !scope.HasAccess(input.Workspace) {
			return nil, ScaleResourceOutput{}, tools.NewScopeError(input.Workspace, scope)
		}

		_, dynClient, err := scope.ClientFor(input.Workspace)
		if err != nil {
			return nil, ScaleResourceOutput{}, fmt.Errorf("getting client: %w", err)
		}

		gvr, err := tools.ParseGVR(input.Group, input.Version, input.Resource)
		if err != nil {
			return nil, ScaleResourceOutput{}, err
		}

		var scaleObj *unstructured.Unstructured
		if input.Namespace != "" {
			scaleObj, err = dynClient.Resource(gvr).Namespace(input.Namespace).Get(ctx, input.Name, metav1.GetOptions{}, "scale")
		} else {
			scaleObj, err = dynClient.Resource(gvr).Get(ctx, input.Name, metav1.GetOptions{}, "scale")
		}
		if err != nil {
			return nil, ScaleResourceOutput{}, fmt.Errorf("getting scale: %w", err)
		}

		currentReplicas := int32(0)
		if spec, ok := scaleObj.Object["spec"].(map[string]any); ok {
			if r, ok := spec["replicas"].(int64); ok {
				currentReplicas = int32(r)
			}
		}

		desiredReplicas := currentReplicas
		message := fmt.Sprintf("Current scale: %d replicas", currentReplicas)

		if input.Replicas != nil {
			desiredReplicas = *input.Replicas

			if err := unstructured.SetNestedField(scaleObj.Object, int64(desiredReplicas), "spec", "replicas"); err != nil {
				return nil, ScaleResourceOutput{}, fmt.Errorf("setting replicas: %w", err)
			}

			if input.Namespace != "" {
				scaleObj, err = dynClient.Resource(gvr).Namespace(input.Namespace).Update(ctx, scaleObj, metav1.UpdateOptions{}, "scale")
			} else {
				scaleObj, err = dynClient.Resource(gvr).Update(ctx, scaleObj, metav1.UpdateOptions{}, "scale")
			}
			if err != nil {
				return nil, ScaleResourceOutput{}, fmt.Errorf("updating scale: %w", err)
			}

			message = fmt.Sprintf("Scaled from %d to %d replicas", currentReplicas, desiredReplicas)
		}

		return nil, ScaleResourceOutput{
			Name:            input.Name,
			Namespace:       input.Namespace,
			CurrentReplicas: currentReplicas,
			DesiredReplicas: desiredReplicas,
			Message:         message,
		}, nil
	})
}

func parseManifest(data string) (*unstructured.Unstructured, error) {
	var rawObj map[string]any

	data = strings.TrimSpace(data)
	if strings.HasPrefix(data, "{") {
		if err := json.Unmarshal([]byte(data), &rawObj); err != nil {
			return nil, fmt.Errorf("invalid JSON: %w", err)
		}
	} else {
		jsonData, err := yaml.YAMLToJSON([]byte(data))
		if err != nil {
			return nil, fmt.Errorf("invalid YAML: %w", err)
		}
		if err := json.Unmarshal(jsonData, &rawObj); err != nil {
			return nil, fmt.Errorf("converting YAML to object: %w", err)
		}
	}

	obj := &unstructured.Unstructured{Object: rawObj}

	if obj.GetAPIVersion() == "" || obj.GetKind() == "" {
		return nil, fmt.Errorf("resource must have apiVersion and kind")
	}

	return obj, nil
}
