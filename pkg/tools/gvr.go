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
)

func parseGVR(apiVersion, kind, group, version, resource string) (schema.GroupVersionResource, error) {
	if apiVersion != "" && kind != "" {
		gv, err := schema.ParseGroupVersion(apiVersion)
		if err != nil {
			return schema.GroupVersionResource{}, fmt.Errorf("invalid apiVersion %q: %w", apiVersion, err)
		}
		res := strings.ToLower(kind)
		if !strings.HasSuffix(res, "s") {
			res += "s"
		}
		switch kind {
		case "Ingress":
			res = "ingresses"
		case "NetworkPolicy":
			res = "networkpolicies"
		case "PodSecurityPolicy":
			res = "podsecuritypolicies"
		case "StorageClass":
			res = "storageclasses"
		case "IngressClass":
			res = "ingressclasses"
		case "Endpoints":
			res = "endpoints"
		}
		return schema.GroupVersionResource{
			Group:    gv.Group,
			Version:  gv.Version,
			Resource: res,
		}, nil
	}

	if version != "" && resource != "" {
		return schema.GroupVersionResource{
			Group:    group,
			Version:  version,
			Resource: resource,
		}, nil
	}

	return schema.GroupVersionResource{}, fmt.Errorf("provide either apiVersion+kind or group+version+resource")
}

func resourceLabel(kind, resource string) string {
	if kind != "" {
		return kind
	}
	return resource
}
