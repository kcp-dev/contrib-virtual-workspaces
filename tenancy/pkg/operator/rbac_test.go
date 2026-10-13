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

package operator

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	tenancyv1alpha1 "github.com/kcp-dev/contrib-virtual-workspaces/tenancy/sdk/apis/tenancy/v1alpha1"
)

func TestEveryRoleCarriesTheAccessVerb(t *testing.T) {
	for _, role := range []tenancyv1alpha1.Role{tenancyv1alpha1.RoleView, tenancyv1alpha1.RoleEdit, tenancyv1alpha1.RoleAdmin} {
		cr, err := DesiredClusterRole(role)
		if err != nil {
			t.Fatalf("DesiredClusterRole(%s): %v", role, err)
		}
		found := false
		for _, rule := range cr.Rules {
			for _, url := range rule.NonResourceURLs {
				if url == "/" || url == "*" {
					for _, v := range rule.Verbs {
						if v == "access" || v == "*" {
							found = true
						}
					}
				}
			}
		}
		if !found {
			t.Errorf("role %s has no workspace-content access rule; subjects could not enter the workspace", role)
		}
	}
}

func TestViewCannotWriteAndEditCannotGrant(t *testing.T) {
	view, _ := DesiredClusterRole(tenancyv1alpha1.RoleView)
	for _, rule := range view.Rules {
		for _, v := range rule.Verbs {
			switch v {
			case "create", "update", "patch", "delete", "*":
				t.Errorf("view role allows %q", v)
			}
		}
	}

	edit, _ := DesiredClusterRole(tenancyv1alpha1.RoleEdit)
	for _, rule := range edit.Rules {
		if len(rule.APIGroups) == 1 && rule.APIGroups[0] == "rbac.authorization.k8s.io" {
			for _, v := range rule.Verbs {
				switch v {
				case "create", "update", "patch", "delete", "*", "bind", "escalate":
					t.Errorf("edit role can mutate RBAC via %q", v)
				}
			}
		}
	}
}

func TestUnknownRoleIsAnError(t *testing.T) {
	if _, err := DesiredClusterRole("root"); err == nil {
		t.Error("expected an error for an unknown role")
	}
}

func TestDesiredBinding(t *testing.T) {
	m := &tenancyv1alpha1.Membership{
		ObjectMeta: metav1.ObjectMeta{Name: "alice-admin"},
		Spec: tenancyv1alpha1.MembershipSpec{
			Subject: tenancyv1alpha1.Subject{Kind: tenancyv1alpha1.SubjectKindUser, Name: "alice"},
			Role:    tenancyv1alpha1.RoleAdmin,
			Tenant:  "acme",
		},
	}
	crb, err := DesiredBinding(m)
	if err != nil {
		t.Fatalf("DesiredBinding: %v", err)
	}
	if crb.Name != MembershipBindingName("alice-admin") {
		t.Errorf("binding name = %q", crb.Name)
	}
	if crb.RoleRef.Name != RoleClusterRoleName(tenancyv1alpha1.RoleAdmin) {
		t.Errorf("roleRef = %q", crb.RoleRef.Name)
	}
	if len(crb.Subjects) != 1 || crb.Subjects[0].Kind != "User" || crb.Subjects[0].Name != "alice" {
		t.Errorf("subjects = %+v", crb.Subjects)
	}
}

func TestDesiredBindingRejectsBadSubjects(t *testing.T) {
	for name, subject := range map[string]tenancyv1alpha1.Subject{
		"empty name":   {Kind: tenancyv1alpha1.SubjectKindUser},
		"unknown kind": {Kind: "ServiceAccount", Name: "sa"},
	} {
		m := &tenancyv1alpha1.Membership{
			ObjectMeta: metav1.ObjectMeta{Name: "m"},
			Spec:       tenancyv1alpha1.MembershipSpec{Subject: subject, Role: tenancyv1alpha1.RoleView, Tenant: "t"},
		}
		if _, err := DesiredBinding(m); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}
