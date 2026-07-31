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

// Package apidomainkey encodes which APIExport a request is being served for.
package apidomainkey

import (
	"context"
	"fmt"
	"strings"

	"github.com/kcp-dev/logicalcluster/v3"
	dynamiccontext "github.com/kcp-dev/virtual-workspace-framework/pkg/dynamic/context"
)

// Key identifies an APIExport by the logical cluster hosting it and its name.
type Key struct {
	Cluster logicalcluster.Name
	Name    string
}

// New builds the API domain key for an APIExport.
func New(cluster logicalcluster.Name, name string) dynamiccontext.APIDomainKey {
	return dynamiccontext.APIDomainKey(fmt.Sprintf("%s/%s", cluster, name))
}

// FromContext extracts the key the root path resolver put in the context.
func FromContext(ctx context.Context) (*Key, error) {
	return Parse(dynamiccontext.APIDomainKeyFrom(ctx))
}

// Parse splits an API domain key back into its parts.
func Parse(key dynamiccontext.APIDomainKey) (*Key, error) {
	parts := strings.Split(string(key), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return nil, fmt.Errorf("invalid APIDomainKey %q for the ephemeral virtual workspace", string(key))
	}

	return &Key{
		Cluster: logicalcluster.Name(parts[0]),
		Name:    parts[1],
	}, nil
}
