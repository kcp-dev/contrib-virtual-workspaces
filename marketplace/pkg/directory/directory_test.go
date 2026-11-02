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

package directory

import (
	"testing"

	"k8s.io/apimachinery/pkg/runtime"

	marketplacev1alpha1 "github.com/kcp-dev/contrib-virtual-workspaces/marketplace/sdk/apis/marketplace/v1alpha1"
)

func TestCatalogJoinsSchemasFromTheOfferingsOwnCluster(t *testing.T) {
	d := New()
	d.UpsertProducer(Key{Cluster: "catalog", Name: "acme"}, Producer{WorkspacePath: "root:acme", WorkspaceCluster: "c-acme"})
	d.UpsertOffering(Key{Cluster: "c-acme", Name: "widgets.acme.example"}, Offering{
		Export: "widgets.acme.example",
		Resources: []ResourceRef{
			{Group: "acme.example", Resource: "widgets", Schema: "v1.widgets.acme.example"},
			{Group: "acme.example", Resource: "gizmos", Schema: "v1.gizmos.acme.example"},
		},
	})
	schema := runtime.RawExtension{Raw: []byte(`{"type":"object"}`)}
	d.UpsertSchema(Key{Cluster: "c-acme", Name: "v1.widgets.acme.example"}, Schema{
		Kind:     "Widget",
		Scope:    "Namespaced",
		Versions: []marketplacev1alpha1.OfferingResourceVersion{{Name: "v1", Served: true, Storage: true, Schema: schema}},
	})
	// A schema of the same name in another workspace is not this offering's.
	d.UpsertSchema(Key{Cluster: "c-other", Name: "v1.gizmos.acme.example"}, Schema{Kind: "Gizmo"})

	entries := d.Catalog()
	if len(entries) != 1 {
		t.Fatalf("catalog = %+v, want one entry", entries)
	}
	got := entries[0].Resources
	if len(got) != 2 {
		t.Fatalf("resources = %+v, want both the export serves", got)
	}
	if got[0].Kind != "Widget" || got[0].Scope != "Namespaced" || len(got[0].Versions) != 1 ||
		string(got[0].Versions[0].Schema.Raw) != `{"type":"object"}` {
		t.Errorf("widgets = %+v, want its schema joined in", got[0])
	}
	if got[1].Resource != "gizmos" || got[1].Kind != "" || got[1].Versions != nil {
		t.Errorf("gizmos = %+v, want it listed without a schema it has not seen", got[1])
	}

	d.RemoveSchema(Key{Cluster: "c-acme", Name: "v1.widgets.acme.example"})
	if r := d.Catalog()[0].Resources[0]; r.Versions != nil {
		t.Errorf("widgets = %+v after its schema went away, want no versions", r)
	}
}
