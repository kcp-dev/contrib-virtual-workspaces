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

package server

import "testing"

func TestRetargetHost(t *testing.T) {
	tests := []struct {
		name string
		host string
		path string
		want string
	}{
		{
			name: "replaces existing cluster path",
			host: "https://kcp.example:6443/clusters/root",
			path: "root:access:controllers",
			want: "https://kcp.example:6443/clusters/root:access:controllers",
		},
		{
			name: "replaces nested cluster path",
			host: "https://kcp.example:6443/clusters/root:orgs:foo",
			path: "root:access",
			want: "https://kcp.example:6443/clusters/root:access",
		},
		{
			name: "appends when no cluster path present",
			host: "https://kcp.example:6443",
			path: "root:access:controllers",
			want: "https://kcp.example:6443/clusters/root:access:controllers",
		},
		{
			name: "handles trailing slash",
			host: "https://kcp.example:6443/",
			path: "root:access",
			want: "https://kcp.example:6443/clusters/root:access",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := retargetHost(tc.host, tc.path); got != tc.want {
				t.Errorf("retargetHost(%q, %q) = %q, want %q", tc.host, tc.path, got, tc.want)
			}
		})
	}
}

func TestValidateWorkspacePath(t *testing.T) {
	o := NewOptions()
	o.WorkspacePath = "not a valid path!"
	if err := o.Validate(); err == nil {
		t.Error("expected error for invalid workspace path")
	}
	o.WorkspacePath = "root:access:controllers"
	if err := o.Validate(); err != nil {
		t.Errorf("unexpected error for valid workspace path: %v", err)
	}
}
