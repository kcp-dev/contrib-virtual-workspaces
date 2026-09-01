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

// Package apis provides read tools for the machinery of kcp's API sharing
// model — APIResourceSchemas and APIConversions. These matter to API
// providers, not to API consumers, so the toolset is opt-in rather than part
// of core.
package apis

import (
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/kcp-dev/contrib-virtual-workspaces/mcp/pkg/tools"
)

// Register adds the apis toolset to server, bound to scope.
func Register(server *mcp.Server, scope tools.Scope) {
	registerAPIResourceSchemaTools(server, scope)
	registerAPIConversionTools(server, scope)
}
