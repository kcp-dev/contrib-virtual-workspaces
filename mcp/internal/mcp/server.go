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
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/kcp-dev/contrib-mcp-virtual-workspace/pkg/toolsets"
)

// NewServer builds an MCP server exposing the named toolsets, bound to scope.
func NewServer(scope *Scope, enabledToolsets []string) (*mcpsdk.Server, error) {
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "kcp", Version: "v1alpha1"}, nil)
	if err := toolsets.Register(server, scope, enabledToolsets); err != nil {
		return nil, err
	}
	return server, nil
}
