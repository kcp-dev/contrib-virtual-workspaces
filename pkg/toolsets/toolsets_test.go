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

package toolsets

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	"github.com/kcp-dev/contrib-mcp-virtual-workspace/pkg/tools"
	"github.com/kcp-dev/contrib-mcp-virtual-workspace/pkg/tools/core"
)

type fakeScope struct {
	clusters []tools.ClusterInfo
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

func (f *fakeScope) Clusters() []tools.ClusterInfo {
	return f.clusters
}

var toolsByToolset = map[string][]string{
	"core": {
		"list_kcp_workspaces",
		"list_kcp_apibindings",
		"get_kcp_apibinding",
		"list_kcp_apiexports",
		"get_kcp_apiexport",
		"list_kcp_workspacetypes",
		"get_kcp_workspacetype",
		"list_kcp_initializingworkspaces",
		"list_kcp_terminatingworkspaces",
		"list_kcp_child_workspaces",
		"list_resources",
		"get_resource",
	},
	"apis": {
		"list_kcp_apiresourceschemas",
		"get_kcp_apiresourceschema",
		"list_kcp_apiconversions",
		"list_kcp_apiexportendpointslices",
	},
	"admin": {
		"list_kcp_logicalclusters",
		"list_kcp_shards",
		"list_kcp_partitions",
		"list_kcp_partitionsets",
	},
	"write": {
		"create_resource",
		"update_resource",
		"patch_resource",
		"delete_resource",
		"scale_resource",
	},
}

func connect(t *testing.T, scope tools.Scope, toolsets []string) *mcp.ClientSession {
	t.Helper()

	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "v0"}, nil)
	if err := Register(server, scope, toolsets); err != nil {
		t.Fatalf("registering toolsets %v: %v", toolsets, err)
	}

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

func listTools(t *testing.T, session *mcp.ClientSession) []string {
	t.Helper()

	var got []string
	for tool, err := range session.Tools(t.Context(), nil) {
		if err != nil {
			t.Fatalf("listing tools: %v", err)
		}
		got = append(got, tool.Name)
	}
	return got
}

func TestEachToolsetExposesExactlyItsTools(t *testing.T) {
	for name, want := range toolsByToolset {
		t.Run(name, func(t *testing.T) {
			got := listTools(t, connect(t, &fakeScope{}, []string{name}))
			slices.Sort(got)
			want := slices.Clone(want)
			slices.Sort(want)
			if !slices.Equal(got, want) {
				t.Errorf("toolset %q registered %v, want %v", name, got, want)
			}
		})
	}
}

func TestAllToolsetsTogether(t *testing.T) {
	var want []string
	for _, names := range toolsByToolset {
		want = append(want, names...)
	}
	got := listTools(t, connect(t, &fakeScope{}, Names()))
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("all toolsets registered %v, want %v", got, want)
	}
}

func TestDefaultIsCoreOnly(t *testing.T) {
	if want := []string{"core"}; !slices.Equal(Default(), want) {
		t.Errorf("Default() = %v, want %v", Default(), want)
	}
}

func TestValidateRejectsUnknownToolsets(t *testing.T) {
	if err := Validate([]string{"core", "nope"}); err == nil {
		t.Fatal("expected error for unknown toolset")
	} else if !strings.Contains(err.Error(), "nope") || !strings.Contains(err.Error(), "core") {
		t.Errorf("error should name the unknown toolset and list valid ones, got: %v", err)
	}

	if err := Validate(Names()); err != nil {
		t.Errorf("Validate(Names()) = %v, want nil", err)
	}
}

func TestRegisterFailsOnUnknownToolset(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "v0"}, nil)
	if err := Register(server, &fakeScope{}, []string{"nope"}); err == nil {
		t.Fatal("expected error for unknown toolset")
	}
}

func TestReadToolsAreAnnotatedReadOnly(t *testing.T) {
	session := connect(t, &fakeScope{}, Names())
	for tool, err := range session.Tools(t.Context(), nil) {
		if err != nil {
			t.Fatalf("listing tools: %v", err)
		}
		wantReadOnly := !slices.Contains(toolsByToolset["write"], tool.Name)
		gotReadOnly := tool.Annotations != nil && tool.Annotations.ReadOnlyHint
		if gotReadOnly != wantReadOnly {
			t.Errorf("tool %q: readOnlyHint = %v, want %v", tool.Name, gotReadOnly, wantReadOnly)
		}
		if tool.Annotations == nil || tool.Annotations.Title == "" {
			t.Errorf("tool %q: missing annotation title", tool.Name)
		}
	}
}

func TestListWorkspacesReturnsScope(t *testing.T) {
	scope := &fakeScope{clusters: []tools.ClusterInfo{
		{ClusterName: "abc123", Endpoint: "https://kcp.example/clusters/abc123"},
	}}
	session := connect(t, scope, Default())

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
	var parsed core.ListWorkspacesOutput
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("unmarshaling output: %v", err)
	}
	if len(parsed.Workspaces) != 1 || parsed.Workspaces[0].ID != "abc123" {
		t.Errorf("unexpected workspaces: %+v", parsed.Workspaces)
	}
}

func TestScopeErrorOnOutOfScopeWorkspace(t *testing.T) {
	session := connect(t, &fakeScope{clusters: []tools.ClusterInfo{
		{ClusterName: "mine", Endpoint: "https://kcp.example/clusters/mine"},
	}}, Default())

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
