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
	"fmt"

	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/kcp-dev/contrib-mcp-virtual-workspace/internal/access"
	"github.com/kcp-dev/contrib-mcp-virtual-workspace/pkg/tools"
)

// ClientFactory produces per-workspace clients that act as the caller through
// impersonation.
type ClientFactory struct {
	base *rest.Config
}

// NewClientFactory returns a factory built on this server's own credential.
func NewClientFactory(base *rest.Config) *ClientFactory {
	return &ClientFactory{base: rest.CopyConfig(base)}
}

// Clients returns typed and dynamic clients for endpoint, impersonating u.
func (f *ClientFactory) Clients(endpoint string, u user.Info) (kubernetes.Interface, dynamic.Interface, error) {
	if endpoint == "" {
		return nil, nil, fmt.Errorf("endpoint must not be empty")
	}
	if u == nil || u.GetName() == "" {
		return nil, nil, fmt.Errorf("user must not be empty")
	}

	cfg := rest.CopyConfig(f.base)
	cfg.Host = endpoint
	cfg.Impersonate = rest.ImpersonationConfig{
		UserName: u.GetName(),
		Groups:   u.GetGroups(),
		Extra:    u.GetExtra(),
	}

	typed, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("building typed client: %w", err)
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("building dynamic client: %w", err)
	}

	return typed, dyn, nil
}

// Scope is the per-request authorization context: who the caller is and which
// workspaces they may reach.
type Scope struct {
	User       user.Info
	Workspaces []access.Workspace

	factory *ClientFactory
}

// Names returns the workspace names in scope.
func (s *Scope) Names() []string {
	names := make([]string, len(s.Workspaces))
	for i, w := range s.Workspaces {
		names[i] = w.Name
	}
	return names
}

// HasAccess reports whether workspace is in scope.
func (s *Scope) HasAccess(workspace string) bool {
	for _, w := range s.Workspaces {
		if w.Name == workspace {
			return true
		}
	}
	return false
}

// Clusters returns the raw workspace access info for the tools package.
func (s *Scope) Clusters() []tools.ClusterInfo {
	clusters := make([]tools.ClusterInfo, len(s.Workspaces))
	for i, w := range s.Workspaces {
		clusters[i] = tools.ClusterInfo{ClusterName: w.Name, Endpoint: w.Endpoint}
	}
	return clusters
}

// ClientFor returns clients for workspace, or an error when it is out of scope.
//
// TODO: clients are rebuilt on every call. A scope lives for a single request
// today, so this costs one build per tool call; if scopes ever outlive a
// request, cache per (endpoint, user) here.
func (s *Scope) ClientFor(workspace string) (kubernetes.Interface, dynamic.Interface, error) {
	for _, w := range s.Workspaces {
		if w.Name == workspace {
			return s.factory.Clients(w.Endpoint, s.User)
		}
	}
	return nil, nil, fmt.Errorf("workspace %q not in scope (available: %v)", workspace, s.Names())
}
