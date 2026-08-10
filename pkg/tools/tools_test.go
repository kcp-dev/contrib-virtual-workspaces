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

package tools

import (
	"encoding/json"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
)

type fakeScope struct {
	clusters []ClusterInfo
}

func (f *fakeScope) Names() []string {
	names := make([]string, len(f.clusters))
	for i, c := range f.clusters {
		names[i] = c.ClusterName
	}
	return names
}

func (f *fakeScope) HasAccess(workspace string) bool {
	for _, c := range f.clusters {
		if c.ClusterName == workspace {
			return true
		}
	}
	return false
}

func (f *fakeScope) ClientFor(string) (kubernetes.Interface, dynamic.Interface, error) {
	return nil, nil, nil
}

func (f *fakeScope) Clusters() []ClusterInfo {
	return f.clusters
}

func connect(t *testing.T, scope Scope) *mcp.ClientSession {
	t.Helper()

	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "v0"}, nil)
	Register(server, scope)

	st, ct := mcp.NewInMemoryTransports()
	if _, err := server.Connect(t.Context(), st, nil); err != nil {
		t.Fatalf("connecting server: %v", err)
	}

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "v0"}, nil)
	session, err := client.Connect(t.Context(), ct, nil)
	if err != nil {
		t.Fatalf("connecting client: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	return session
}

func TestRegisterExposesKcpObjectTools(t *testing.T) {
	session := connect(t, &fakeScope{})

	want := map[string]bool{
		"list_kcp_workspaces":              false,
		"list_kcp_apiexports":              false,
		"get_kcp_apiexport":                false,
		"list_kcp_apiresourceschemas":      false,
		"get_kcp_apiresourceschema":        false,
		"list_kcp_apibindings":             false,
		"get_kcp_apibinding":               false,
		"list_kcp_apiconversions":          false,
		"list_kcp_apiexportendpointslices": false,
		"list_kcp_workspacetypes":          false,
		"get_kcp_workspacetype":            false,
		"list_kcp_initializingworkspaces":  false,
		"list_kcp_terminatingworkspaces":   false,
		"list_kcp_child_workspaces":        false,
		"list_kcp_logicalclusters":         false,
		"list_kcp_shards":                  false,
		"list_kcp_partitions":              false,
		"list_kcp_partitionsets":           false,
		"list_kcp_synctargets":             false,
		"list_kcp_placements":              false,
		"list_kcp_locations":               false,
		"create_resource":                  false,
		"update_resource":                  false,
		"patch_resource":                   false,
		"delete_resource":                  false,
		"scale_resource":                   false,
	}

	var got []string
	for tool, err := range session.Tools(t.Context(), nil) {
		if err != nil {
			t.Fatalf("listing tools: %v", err)
		}
		got = append(got, tool.Name)
		if _, ok := want[tool.Name]; ok {
			want[tool.Name] = true
		}
	}

	for name, seen := range want {
		if !seen {
			t.Errorf("tool %q not registered (got %v)", name, got)
		}
	}
	if len(got) != len(want) {
		t.Errorf("registered %d tools, want %d: %v", len(got), len(want), got)
	}
}

func TestListWorkspacesReturnsScope(t *testing.T) {
	scope := &fakeScope{clusters: []ClusterInfo{
		{ClusterName: "abc123", Endpoint: "https://kcp.example/clusters/abc123"},
	}}
	session := connect(t, scope)

	res, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "list_kcp_workspaces"})
	if err != nil {
		t.Fatalf("calling list_kcp_workspaces: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool returned error: %v", res.Content)
	}

	out, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("marshaling output: %v", err)
	}
	var parsed ListWorkspacesOutput
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("unmarshaling output: %v", err)
	}
	if len(parsed.Workspaces) != 1 || parsed.Workspaces[0].ID != "abc123" {
		t.Errorf("unexpected workspaces: %+v", parsed.Workspaces)
	}
}

func TestScopeErrorOnOutOfScopeWorkspace(t *testing.T) {
	session := connect(t, &fakeScope{clusters: []ClusterInfo{
		{ClusterName: "mine", Endpoint: "https://kcp.example/clusters/mine"},
	}})

	res, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "list_kcp_apiexports",
		Arguments: map[string]any{"workspace": "not-mine"},
	})
	if err != nil {
		t.Fatalf("calling list_kcp_apiexports: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected error result for out-of-scope workspace")
	}
}
