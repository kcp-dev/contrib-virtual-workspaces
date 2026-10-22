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

package membership

import (
	"context"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apiserver/pkg/authentication/user"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"

	"github.com/kcp-dev/contrib-virtual-workspaces/tenancy/pkg/directory"
	tenancyv1alpha1 "github.com/kcp-dev/contrib-virtual-workspaces/tenancy/sdk/apis/tenancy/v1alpha1"
)

// This storage decides who may see and change other people's access, so
// what it refuses is the interesting part.

// readyDirectory: alice administers acme, carol administers other, bob
// only views one of acme's projects, and mallory is a member of nothing.
func readyDirectory() *directory.Directory {
	d := directory.New()
	d.UpsertTenant(directory.Key{Cluster: "platform", Name: "acme"},
		directory.Tenant{DisplayName: "Acme", WorkspaceCluster: "ws1"})
	d.UpsertTenant(directory.Key{Cluster: "platform", Name: "other"},
		directory.Tenant{DisplayName: "Other", WorkspaceCluster: "ws2"})
	d.UpsertProject(directory.Key{Cluster: "ws1", Name: "web"},
		directory.Project{Tenant: "acme", WorkspaceCluster: "ws-web"})

	d.UpsertMembership(directory.Key{Cluster: "ws1", Name: "alice-acme"},
		directory.Membership{
			Subject: tenancyv1alpha1.Subject{Kind: tenancyv1alpha1.SubjectKindUser, Name: "alice"},
			Role:    "admin", Tenant: "acme",
		})
	// A second admin, so revoking one is not revoking the last.
	d.UpsertMembership(directory.Key{Cluster: "ws1", Name: "dave-acme"},
		directory.Membership{
			Subject: tenancyv1alpha1.Subject{Kind: tenancyv1alpha1.SubjectKindUser, Name: "dave"},
			Role:    "admin", Tenant: "acme",
		})
	d.UpsertMembership(directory.Key{Cluster: "ws1", Name: "bob-acme-web"},
		directory.Membership{
			Subject: tenancyv1alpha1.Subject{Kind: tenancyv1alpha1.SubjectKindUser, Name: "bob"},
			Role:    "view", Tenant: "acme", Project: "web",
		})
	d.UpsertMembership(directory.Key{Cluster: "ws2", Name: "carol-other"},
		directory.Membership{
			Subject: tenancyv1alpha1.Subject{Kind: tenancyv1alpha1.SubjectKindUser, Name: "carol"},
			Role:    "admin", Tenant: "other",
		})
	d.SetReady()
	return d
}

func ctxWithUser(name string) context.Context {
	return genericapirequest.WithUser(context.Background(), &user.DefaultInfo{Name: name})
}

// recorder captures what the storage asked to have written.
type recorder struct {
	cluster    string
	grant      *tenancyv1alpha1.Membership
	created    bool
	creates    int
	deleted    string
	delCluster string
	deletes    int
}

func (r *recorder) create(_ context.Context, cluster string, m *tenancyv1alpha1.Membership) (bool, error) {
	r.creates++
	r.cluster, r.grant = cluster, m
	return r.created, nil
}

func (r *recorder) delete(_ context.Context, cluster, name string) error {
	r.deletes++
	r.delCluster, r.deleted = cluster, name
	return nil
}

func (r *recorder) options() Options { return Options{Create: r.create, Delete: r.delete} }

func grantFor(tenant, project, subject string, role tenancyv1alpha1.Role) *tenancyv1alpha1.Membership {
	return &tenancyv1alpha1.Membership{
		Spec: tenancyv1alpha1.MembershipSpec{
			Subject: tenancyv1alpha1.Subject{Kind: tenancyv1alpha1.SubjectKindUser, Name: subject},
			Role:    role, Tenant: tenant, Project: project,
		},
	}
}

func listFor(t *testing.T, r *REST, username string) *tenancyv1alpha1.MembershipList {
	t.Helper()
	obj, err := r.List(ctxWithUser(username), nil)
	if err != nil {
		t.Fatalf("List as %s: %v", username, err)
	}
	list, ok := obj.(*tenancyv1alpha1.MembershipList)
	if !ok {
		t.Fatalf("List returned %T", obj)
	}
	return list
}

func TestAdminSeesEveryGrantInTheirTenant(t *testing.T) {
	rec := &recorder{}
	r := NewREST(readyDirectory(), rec.options())

	list := listFor(t, r, "alice")
	if len(list.Items) != 3 {
		t.Fatalf("alice sees %d grants, want acme's 3: %+v", len(list.Items), list.Items)
	}
	// Including grants that are not her own — that is the point of
	// administering access.
	var subjects []string
	for _, m := range list.Items {
		subjects = append(subjects, m.Spec.Subject.Name)
		if m.Spec.Tenant != "acme" {
			t.Errorf("alice sees a grant on tenant %q", m.Spec.Tenant)
		}
	}
	t.Logf("alice sees grants for %v", subjects)
}

func TestNonAdminsSeeNothing(t *testing.T) {
	rec := &recorder{}
	r := NewREST(readyDirectory(), rec.options())

	for _, who := range []string{"bob", "mallory"} {
		list := listFor(t, r, who)
		if len(list.Items) != 0 {
			t.Errorf("%s sees %d grants, want none: %+v", who, len(list.Items), list.Items)
		}
	}
}

func TestListIsScopedToTheTenantsYouAdminister(t *testing.T) {
	rec := &recorder{}
	r := NewREST(readyDirectory(), rec.options())

	list := listFor(t, r, "carol")
	if len(list.Items) != 1 || list.Items[0].Spec.Tenant != "other" {
		t.Fatalf("carol sees %+v, want only her own tenant's grant", list.Items)
	}
}

func TestAdminGrantsInTheTenantWorkspace(t *testing.T) {
	rec := &recorder{created: true}
	r := NewREST(readyDirectory(), rec.options())

	obj, err := r.Create(ctxWithUser("alice"), grantFor("acme", "", "erin@example.com", tenancyv1alpha1.RoleEdit), nil, nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// The cluster comes from the directory, never from the request.
	if rec.cluster != "ws1" {
		t.Errorf("wrote to cluster %q, want the tenant's own ws1", rec.cluster)
	}
	// The name is derived, so granting the same access twice is one object.
	if rec.grant.Name != "erin-example-com-acme" {
		t.Errorf("derived name %q", rec.grant.Name)
	}
	if got := obj.(*tenancyv1alpha1.Membership).Name; got != rec.grant.Name {
		t.Errorf("returned name %q, want %q", got, rec.grant.Name)
	}
}

func TestCallerCannotChooseTheObjectName(t *testing.T) {
	rec := &recorder{created: true}
	r := NewREST(readyDirectory(), rec.options())

	grant := grantFor("acme", "", "erin@example.com", tenancyv1alpha1.RoleView)
	grant.Name = "something-else"
	if _, err := r.Create(ctxWithUser("alice"), grant, nil, nil); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if rec.grant.Name != "erin-example-com-acme" {
		t.Errorf("name %q followed the request instead of the grant", rec.grant.Name)
	}
}

func TestNonAdminCannotGrant(t *testing.T) {
	for _, who := range []string{"bob", "mallory"} {
		rec := &recorder{}
		r := NewREST(readyDirectory(), rec.options())

		_, err := r.Create(ctxWithUser(who), grantFor("acme", "", "erin", tenancyv1alpha1.RoleAdmin), nil, nil)
		if !apierrors.IsForbidden(err) {
			t.Errorf("%s granting: %v, want Forbidden", who, err)
		}
		if rec.creates != 0 {
			t.Errorf("%s reached the writer", who)
		}
	}
}

func TestAdminOfOneTenantCannotGrantInAnother(t *testing.T) {
	rec := &recorder{}
	r := NewREST(readyDirectory(), rec.options())

	// carol administers "other", and must not be able to reach "acme" —
	// nor learn from the refusal whether it exists.
	_, err := r.Create(ctxWithUser("carol"), grantFor("acme", "", "carol", tenancyv1alpha1.RoleAdmin), nil, nil)
	if !apierrors.IsForbidden(err) {
		t.Fatalf("cross-tenant grant: %v, want Forbidden", err)
	}
	if rec.creates != 0 {
		t.Error("cross-tenant grant reached the writer")
	}
}

func TestGrantOnAnUnknownProjectIsRefused(t *testing.T) {
	rec := &recorder{}
	r := NewREST(readyDirectory(), rec.options())

	_, err := r.Create(ctxWithUser("alice"), grantFor("acme", "nope", "erin", tenancyv1alpha1.RoleView), nil, nil)
	if !apierrors.IsInvalid(err) {
		t.Fatalf("grant on a missing project: %v, want Invalid", err)
	}
	if rec.creates != 0 {
		t.Error("it reached the writer anyway")
	}
}

func TestInvalidGrantsAreRefusedBeforeTheWrite(t *testing.T) {
	cases := map[string]*tenancyv1alpha1.Membership{
		"no subject":          grantFor("acme", "", "", tenancyv1alpha1.RoleView),
		"unsluggable subject": grantFor("acme", "", "!!!", tenancyv1alpha1.RoleView),
		"unknown role":        grantFor("acme", "", "erin", tenancyv1alpha1.Role("root")),
		"no tenant":           grantFor("", "", "erin", tenancyv1alpha1.RoleView),
	}
	for name, grant := range cases {
		rec := &recorder{}
		r := NewREST(readyDirectory(), rec.options())

		_, err := r.Create(ctxWithUser("alice"), grant, nil, nil)
		if !apierrors.IsInvalid(err) {
			t.Errorf("%s: %v, want Invalid", name, err)
		}
		if rec.creates != 0 {
			t.Errorf("%s: reached the writer", name)
		}
	}
}

func TestAdminRevokesAGrant(t *testing.T) {
	rec := &recorder{}
	r := NewREST(readyDirectory(), rec.options())

	_, deleted, err := r.Delete(ctxWithUser("alice"), "bob-acme-web", nil, nil)
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if !deleted {
		t.Error("Delete reported nothing removed")
	}
	if rec.delCluster != "ws1" || rec.deleted != "bob-acme-web" {
		t.Errorf("deleted %q in %q", rec.deleted, rec.delCluster)
	}
}

func TestTheLastAdminCannotBeRevoked(t *testing.T) {
	d := readyDirectory()
	// Leave acme with a single admin.
	d.RemoveMembership(directory.Key{Cluster: "ws1", Name: "dave-acme"})

	rec := &recorder{}
	r := NewREST(d, rec.options())

	_, _, err := r.Delete(ctxWithUser("alice"), "alice-acme", nil, nil)
	if !apierrors.IsForbidden(err) {
		t.Fatalf("revoking the last admin: %v, want Forbidden", err)
	}
	if rec.deletes != 0 {
		t.Error("it reached the writer, leaving the tenant unadministerable")
	}
}

func TestRevokingSomethingYouDoNotAdministerIsNotFound(t *testing.T) {
	rec := &recorder{}
	r := NewREST(readyDirectory(), rec.options())

	// carol administers another tenant: acme's grants are not hers to see,
	// so they are reported as absent rather than refused.
	_, _, err := r.Delete(ctxWithUser("carol"), "bob-acme-web", nil, nil)
	if !apierrors.IsNotFound(err) {
		t.Fatalf("cross-tenant revoke: %v, want NotFound", err)
	}
	if rec.deletes != 0 {
		t.Error("cross-tenant revoke reached the writer")
	}
}

func TestReadOnlyDeploymentRefusesWrites(t *testing.T) {
	r := NewREST(readyDirectory(), Options{})

	if _, err := r.Create(ctxWithUser("alice"), grantFor("acme", "", "erin", tenancyv1alpha1.RoleView), nil, nil); !apierrors.IsServiceUnavailable(err) {
		t.Errorf("Create without a writer: %v, want ServiceUnavailable", err)
	}
	if _, _, err := r.Delete(ctxWithUser("alice"), "bob-acme-web", nil, nil); !apierrors.IsServiceUnavailable(err) {
		t.Errorf("Delete without a writer: %v, want ServiceUnavailable", err)
	}
}

func TestAnUnsyncedDirectoryAnswersNothing(t *testing.T) {
	rec := &recorder{}
	r := NewREST(directory.New(), rec.options())

	if _, err := r.List(ctxWithUser("alice"), nil); !apierrors.IsServiceUnavailable(err) {
		t.Errorf("List before sync: %v, want ServiceUnavailable", err)
	}
}

func TestGetOnlyReachesYourOwnTenants(t *testing.T) {
	rec := &recorder{}
	r := NewREST(readyDirectory(), rec.options())

	if _, err := r.Get(ctxWithUser("alice"), "bob-acme-web", nil); err != nil {
		t.Fatalf("alice getting a grant in acme: %v", err)
	}
	if _, err := r.Get(ctxWithUser("carol"), "bob-acme-web", nil); !apierrors.IsNotFound(err) {
		t.Errorf("carol getting a grant in acme: %v, want NotFound", err)
	}
}
