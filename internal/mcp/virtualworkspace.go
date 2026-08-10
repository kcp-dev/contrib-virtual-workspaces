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

// Package mcp serves the Model Context Protocol as a kcp virtual workspace.
package mcp

import (
	"context"
	"net/http"
	"strings"

	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/client-go/rest"

	"github.com/kcp-dev/virtual-workspace-framework/framework"
	frameworkhandler "github.com/kcp-dev/virtual-workspace-framework/pkg/handler"
	"github.com/kcp-dev/virtual-workspace-framework/pkg/rootapiserver"

	"github.com/kcp-dev/contrib-mcp-virtual-workspace/internal/access"
)

// VirtualWorkspaceName is the name this virtual workspace is registered under.
const VirtualWorkspaceName = "mcp"

// RootPath is the URL prefix kcp's front-proxy routes here.
const RootPath = "/services/" + VirtualWorkspaceName

// NewVirtualWorkspace registers MCP as a raw-handler virtual workspace.
//
// Raw rather than a delegated apiserver because MCP is JSON-RPC over streamable
// HTTP: there is no group, version or resource for the fixed-group-version
// machinery to serve. It still sits behind the root apiserver's filter chain,
// so authentication and audit are the framework's, not ours.
func NewVirtualWorkspace(accessClient *access.Client, impersonator *rest.Config) rootapiserver.NamedVirtualWorkspace {
	vw := &frameworkhandler.VirtualWorkspace{
		RootPathResolver: framework.RootPathResolverFunc(func(urlPath string, ctx context.Context) (bool, string, context.Context) {
			if urlPath != RootPath && !strings.HasPrefix(urlPath, RootPath+"/") {
				return false, "", ctx
			}
			return true, RootPath, ctx
		}),
		Authorizer:   authenticatedOnly(),
		ReadyChecker: framework.ReadyFunc(func() error { return nil }),
		HandlerFactory: frameworkhandler.HandlerFactory(func(genericapiserver.CompletedConfig) (http.Handler, error) {
			return NewHandler(accessClient, NewClientFactory(impersonator)), nil
		}),
	}

	return rootapiserver.NamedVirtualWorkspace{
		Name:             VirtualWorkspaceName,
		VirtualWorkspace: vw,
	}
}
