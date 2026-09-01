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
	"fmt"
	"net/http"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/contrib-virtual-workspaces/mcp/internal/access"
)

// NewHandler returns the MCP handler.
//
// Stateless: every request rebuilds the caller's Scope, so a workspace granted
// or revoked between calls takes effect within the access cache's TTL rather
// than lasting for the lifetime of a session.
func NewHandler(accessClient *access.Client, factory *ClientFactory, toolsets []string) http.Handler {
	return mcpsdk.NewStreamableHTTPHandler(
		func(r *http.Request) *mcpsdk.Server {
			return serverForRequest(r, accessClient, factory, toolsets)
		},
		&mcpsdk.StreamableHTTPOptions{Stateless: true},
	)
}

func serverForRequest(r *http.Request, accessClient *access.Client, factory *ClientFactory, toolsets []string) *mcpsdk.Server {
	ctx := r.Context()
	logger := klog.FromContext(ctx)

	u, ok := genericapirequest.UserFrom(ctx)
	if !ok {
		logger.Error(nil, "no user in request context; did the authentication filter run?")
		return errorServer("no authenticated user in request context")
	}

	workspaces, err := accessClient.Workspaces(ctx, u)
	if err != nil {
		logger.Error(err, "resolving caller workspaces", "username", u.GetName())
		return errorServer("could not resolve accessible workspaces: " + err.Error())
	}

	server, err := NewServer(&Scope{User: u, Workspaces: workspaces, factory: factory}, toolsets)
	if err != nil {
		logger.Error(err, "registering toolsets")
		return errorServer("could not register toolsets: " + err.Error())
	}
	return server
}

func errorServer(msg string) *mcpsdk.Server {
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "kcp", Version: "v1alpha1"}, nil)

	type input struct{}
	type output struct {
		Error string `json:"error"`
	}

	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:        "error",
		Description: "Returns why the MCP server is unavailable",
	}, func(context.Context, *mcpsdk.CallToolRequest, input) (*mcpsdk.CallToolResult, output, error) {
		return &mcpsdk.CallToolResult{IsError: true}, output{Error: fmt.Sprintf("MCP server unavailable: %s", msg)}, nil
	})

	return server
}
