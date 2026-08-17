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

// Package toolsets names the groups of MCP tools an operator can enable.
//
// The selection is enforced server-side: a toolset that is not enabled is
// never registered, so no client, model or prompt can reach it. The default
// is the read-only core toolset; everything else is opt-in.
package toolsets

import (
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/kcp-dev/contrib-mcp-virtual-workspace/pkg/tools"
	"github.com/kcp-dev/contrib-mcp-virtual-workspace/pkg/tools/admin"
	"github.com/kcp-dev/contrib-mcp-virtual-workspace/pkg/tools/apis"
	"github.com/kcp-dev/contrib-mcp-virtual-workspace/pkg/tools/core"
	"github.com/kcp-dev/contrib-mcp-virtual-workspace/pkg/tools/write"
)

// Toolset is a named group of MCP tools that is enabled or disabled as a unit.
type Toolset struct {
	Name        string
	Description string
	Register    func(*mcp.Server, tools.Scope)
}

// AllToolSets returns every toolset, in registration order.
func AllToolsets() []Toolset {
	return []Toolset{
		{
			Name:        "core",
			Description: "Read-only workspace navigation and generic resource reads",
			Register:    core.Register,
		},
		{
			Name:        "apis",
			Description: "Read-only API provider machinery: APIResourceSchemas, APIConversions, APIExportEndpointSlices",
			Register:    apis.Register,
		},
		{
			Name:        "admin",
			Description: "Read-only kcp operator internals: LogicalClusters, Shards, Partitions, PartitionSets",
			Register:    admin.Register,
		},
		{
			Name:        "write",
			Description: "Generic resource mutations: create, update, patch, delete, scale",
			Register:    write.Register,
		},
	}
}

// Default is the toolset selection used when the operator does not choose one.
func Default() []string {
	return []string{"core"}
}

// Names returns the valid toolset names.
func Names() []string {
	all := AllToolsets()
	names := make([]string, len(all))
	for i, ts := range all {
		names[i] = ts.Name
	}
	return names
}

// Validate rejects unknown toolset names, listing the valid ones.
func Validate(names []string) error {
	valid := make(map[string]bool, len(names))
	for _, name := range Names() {
		valid[name] = true
	}
	var unknown []string
	for _, name := range names {
		if !valid[name] {
			unknown = append(unknown, name)
		}
	}
	if len(unknown) > 0 {
		return fmt.Errorf("unknown toolset(s) %s; valid toolsets are %s",
			strings.Join(unknown, ", "), strings.Join(Names(), ", "))
	}
	return nil
}

// Register adds the named toolsets to server, bound to scope.
func Register(server *mcp.Server, scope tools.Scope, names []string) error {
	if err := Validate(names); err != nil {
		return err
	}
	enabled := make(map[string]bool, len(names))
	for _, name := range names {
		enabled[name] = true
	}
	for _, ts := range AllToolsets() {
		if enabled[ts.Name] {
			ts.Register(server, scope)
		}
	}
	return nil
}
