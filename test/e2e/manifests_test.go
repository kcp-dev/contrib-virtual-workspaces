//go:build e2e

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

package e2e

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"
)

// TestManifestsRender renders every embedded manifest and parses the result.
//
// It needs no cluster, and Go runs it before the deployment test, so a typo in a template or a
// field name that no longer exists fails in seconds rather than after kcp has been stood up. It
// also catches the mistake the templates are most prone to: a placeholder that no longer matches
// a field of templateData, which `missingkey=error` turns into a render failure here.
func TestManifestsRender(t *testing.T) {
	data := templateData{
		Namespace:                "e2e-render",
		FrontProxyHostname:       "front-proxy-front-proxy.e2e-render.svc.cluster.local",
		VirtualWorkspaceName:     virtualWorkspaceName,
		ImageRepository:          "localhost/access-vw",
		ImageTag:                 "e2e",
		WorkspacePrefix:          workspacePrefix,
		ControllersWorkspaceName: controllersWorkspaceName,
		ControllersWorkspace:     controllersWorkspace,
		APIExportName:            apiExportName,
		ServerUsername:           serverUsername,
		ConsumerUsername:         consumerUsername,
	}

	manifests := []string{
		"etcd.yaml.tmpl",
		"kcp.yaml.tmpl",
		"virtualworkspace.yaml.tmpl",
		"server-rbac.yaml.tmpl",
		"consumer.yaml.tmpl",
	}

	for _, name := range manifests {
		t.Run(name, func(t *testing.T) {
			docs := splitYAML(t, render(t, name, data))
			if len(docs) == 0 {
				t.Fatal("Rendered to no documents.")
			}

			for i, doc := range docs {
				u := &unstructured.Unstructured{}
				if err := yaml.Unmarshal(doc, &u.Object); err != nil {
					t.Errorf("Document %d is not valid YAML: %v", i+1, err)

					continue
				}

				if u.GetKind() == "" || u.GetAPIVersion() == "" {
					t.Errorf("Document %d has no kind or apiVersion: %v", i+1, u.Object)
				}
				if u.GetName() == "" {
					t.Errorf("Document %d (%s) has no name.", i+1, u.GetKind())
				}
			}
		})
	}
}
