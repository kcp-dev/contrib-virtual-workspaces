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

package mcp

import (
	"context"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// NewServer builds an MCP server whose tools are bound to scope.
//
// TODO: register the tool handlers ported from the proof of concept —
// list/get/create/update/delete over the caller's workspaces, plus the
// kcp-specific workspace tools. Until then the server advertises only
// list_workspaces, which is enough to verify the access-VW round trip and the
// impersonation path end to end.
func NewServer(scope *Scope) *mcpsdk.Server {
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "kcp", Version: "v1alpha1"}, nil)

	type input struct{}
	type workspace struct {
		Name     string `json:"name"`
		Endpoint string `json:"endpoint"`
	}
	type output struct {
		Workspaces []workspace `json:"workspaces"`
	}

	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:        "list_workspaces",
		Description: "Lists the kcp workspaces the calling user can access",
	}, func(context.Context, *mcpsdk.CallToolRequest, input) (*mcpsdk.CallToolResult, output, error) {
		out := output{Workspaces: make([]workspace, 0, len(scope.Workspaces))}
		for _, w := range scope.Workspaces {
			out.Workspaces = append(out.Workspaces, workspace{Name: w.Name, Endpoint: w.Endpoint})
		}
		return nil, out, nil
	})

	return server
}
