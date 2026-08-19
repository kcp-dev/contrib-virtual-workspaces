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
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
)

// ParseGVR builds a GroupVersionResource from explicit tool input. Group is
// empty for the core API group; version and resource are required.
func ParseGVR(group, version, resource string) (schema.GroupVersionResource, error) {
	if version == "" || resource == "" {
		return schema.GroupVersionResource{}, fmt.Errorf("version and resource are required (group is empty for the core API group)")
	}
	return schema.GroupVersionResource{
		Group:    group,
		Version:  version,
		Resource: resource,
	}, nil
}

// ResolveGVR maps a manifest's apiVersion and kind to a resource using the
// workspace's own discovery. Guessing the plural from the kind would silently
// produce a wrong resource for CRDs with irregular plurals; discovery is exact
// and reflects exactly the APIs bound into that workspace.
func ResolveGVR(disc discovery.DiscoveryInterface, apiVersion, kind string) (schema.GroupVersionResource, error) {
	gv, err := schema.ParseGroupVersion(apiVersion)
	if err != nil {
		return schema.GroupVersionResource{}, fmt.Errorf("invalid apiVersion %q: %w", apiVersion, err)
	}

	resources, err := disc.ServerResourcesForGroupVersion(apiVersion)
	if err != nil {
		return schema.GroupVersionResource{}, fmt.Errorf("discovering resources for %q: %w", apiVersion, err)
	}

	for _, r := range resources.APIResources {
		if r.Kind == kind && !strings.Contains(r.Name, "/") {
			return gv.WithResource(r.Name), nil
		}
	}

	return schema.GroupVersionResource{}, fmt.Errorf("no resource for kind %q in %q", kind, apiVersion)
}
