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

package offering

import (
	"reflect"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"

	marketplacev1alpha1 "github.com/kcp-dev/contrib-virtual-workspaces/marketplace/sdk/apis/marketplace/v1alpha1"
)

func exportWith(annotations map[string]string, claims ...apisv1alpha2.PermissionClaim) *apisv1alpha2.APIExport {
	return &apisv1alpha2.APIExport{
		ObjectMeta: metav1.ObjectMeta{Name: "wildwest", Annotations: annotations},
		Spec:       apisv1alpha2.APIExportSpec{PermissionClaims: claims},
		Status:     apisv1alpha2.APIExportStatus{IdentityHash: "abc123"},
	}
}

func claim(group, resource string, verbs ...string) apisv1alpha2.PermissionClaim {
	return apisv1alpha2.PermissionClaim{
		GroupResource: apisv1alpha2.GroupResource{Group: group, Resource: resource},
		Verbs:         verbs,
	}
}

func TestOnlyTruePublishes(t *testing.T) {
	cases := map[string]bool{
		"true":  true,
		"True":  false,
		"yes":   false,
		"1":     false,
		"false": false,
		"":      false,
	}
	for value, want := range cases {
		got := Published(exportWith(map[string]string{marketplacev1alpha1.AnnotationPublish: value}))
		if got != want {
			t.Errorf("publish=%q published=%v, want %v", value, got, want)
		}
	}
	// A typo must leave an export off the catalog, because being on it is
	// what hands `bind` to strangers.
	if Published(exportWith(nil)) {
		t.Error("an export with no annotations is published")
	}
	if Published(nil) {
		t.Error("a nil export is published")
	}
}

func TestDisplayNameFallsBackToTheExportName(t *testing.T) {
	card := From(exportWith(map[string]string{marketplacev1alpha1.AnnotationPublish: "true"}))
	if card.DisplayName != "wildwest" {
		t.Errorf("display name %q, want the export's own name", card.DisplayName)
	}

	card = From(exportWith(map[string]string{
		marketplacev1alpha1.AnnotationPublish:     "true",
		marketplacev1alpha1.AnnotationDisplayName: "Wild West",
		marketplacev1alpha1.AnnotationDescription: "cowboys",
	}))
	if card.DisplayName != "Wild West" || card.Description != "cowboys" {
		t.Errorf("card = %+v", card)
	}
}

func TestClaimsAreTheExportsOwn(t *testing.T) {
	export := exportWith(map[string]string{marketplacev1alpha1.AnnotationPublish: "true"},
		claim("", "secrets", "get", "list"),
		claim("wildwest.dev", "sheriffs", "*"),
	)
	export.Spec.PermissionClaims[0].IdentityHash = "core"

	card := From(export)
	if len(card.Claims) != 2 {
		t.Fatalf("claims = %+v", card.Claims)
	}
	// Sorted by group then resource: the core-group claim comes first.
	if card.Claims[0].Resource != "secrets" || card.Claims[0].IdentityHash != "core" {
		t.Errorf("first claim = %+v", card.Claims[0])
	}
	if !reflect.DeepEqual(card.Claims[0].Verbs, []string{"get", "list"}) {
		t.Errorf("verbs = %v", card.Claims[0].Verbs)
	}
	if card.IdentityHash != "abc123" {
		t.Errorf("identity hash = %q", card.IdentityHash)
	}
}

func TestSuggestedScopeComesFromTheDefaultSelector(t *testing.T) {
	export := exportWith(map[string]string{marketplacev1alpha1.AnnotationPublish: "true"},
		claim("", "secrets", "get"))
	export.Spec.PermissionClaims[0].DefaultSelector = &apisv1alpha2.PermissionClaimSelector{
		LabelSelector: metav1.LabelSelector{MatchLabels: map[string]string{"owner": "wildwest"}},
	}

	card := From(export)
	if got := card.Claims[0].MatchLabels["owner"]; got != "wildwest" {
		t.Errorf("match labels = %v", card.Claims[0].MatchLabels)
	}

	// An unset selector is not a narrower scope: it is every object, which
	// is what a binding defaults to anyway.
	export.Spec.PermissionClaims[0].DefaultSelector = nil
	if labels := From(export).Claims[0].MatchLabels; labels != nil {
		t.Errorf("match labels = %v, want none", labels)
	}
}

func TestRevisionFollowsContentNotOrder(t *testing.T) {
	published := map[string]string{marketplacev1alpha1.AnnotationPublish: "true"}
	a := From(exportWith(published,
		claim("wildwest.dev", "sheriffs", "get"),
		claim("", "secrets", "get"),
	))
	b := From(exportWith(published,
		claim("", "secrets", "get"),
		claim("wildwest.dev", "sheriffs", "get"),
	))
	if a.Revision != b.Revision {
		t.Errorf("reordering the claims changed the revision: %q vs %q", a.Revision, b.Revision)
	}
	if a.Revision == "" {
		t.Fatal("no revision for a claim surface that has claims")
	}

	// Verb order is not a change either; a new verb is.
	same := From(exportWith(published, claim("", "secrets", "list", "get")))
	also := From(exportWith(published, claim("", "secrets", "get", "list")))
	if same.Revision != also.Revision {
		t.Errorf("verb order changed the revision: %q vs %q", same.Revision, also.Revision)
	}
	wider := From(exportWith(published, claim("", "secrets", "get", "list", "create")))
	if wider.Revision == same.Revision {
		t.Error("widening the verbs did not change the revision")
	}
}

func TestScopeChangesTheRevision(t *testing.T) {
	published := map[string]string{marketplacev1alpha1.AnnotationPublish: "true"}
	unscoped := exportWith(published, claim("", "secrets", "get"))
	scoped := exportWith(published, claim("", "secrets", "get"))
	scoped.Spec.PermissionClaims[0].DefaultSelector = &apisv1alpha2.PermissionClaimSelector{
		LabelSelector: metav1.LabelSelector{MatchLabels: map[string]string{"owner": "wildwest"}},
	}
	if From(unscoped).Revision == From(scoped).Revision {
		t.Error("narrowing a claim to a label scope did not change the revision")
	}
}

func TestNoClaimsMeansNoRevision(t *testing.T) {
	card := From(exportWith(map[string]string{marketplacev1alpha1.AnnotationPublish: "true"}))
	if card.Claims != nil {
		t.Errorf("claims = %+v, want none", card.Claims)
	}
	if card.Revision != "" {
		t.Errorf("revision = %q, want empty for an offering that claims nothing", card.Revision)
	}
}
