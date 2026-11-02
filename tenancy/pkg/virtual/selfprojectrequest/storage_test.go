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

package selfprojectrequest

import (
	"context"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apiserver/pkg/authentication/user"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"

	"github.com/kcp-dev/contrib-virtual-workspaces/tenancy/pkg/directory"
	tenancyv1alpha1 "github.com/kcp-dev/contrib-virtual-workspaces/tenancy/sdk/apis/tenancy/v1alpha1"
)

// This storage holds a credential its callers do not, so what it refuses
// matters more than what it allows. The tests are mostly refusals.

// readyDirectory: alice administers acme, bob only views one of its
// projects, and mallory is a member of nothing.
func readyDirectory() *directory.Directory {
	d := directory.New()
	d.UpsertTenant(directory.Key{Cluster: "platform", Name: "acme"},
		directory.Tenant{DisplayName: "Acme", WorkspaceCluster: "ws1"})
	d.UpsertProject(directory.Key{Cluster: "ws1", Name: "web"},
		directory.Project{Tenant: "acme", WorkspaceCluster: "ws-web"})
	d.UpsertMembership(directory.Key{Cluster: "ws1", Name: "m-alice"},
		directory.Membership{
			Subject: tenancyv1alpha1.Subject{Kind: tenancyv1alpha1.SubjectKindUser, Name: "alice"},
			Role:    "admin", Tenant: "acme",
		})
	d.UpsertMembership(directory.Key{Cluster: "ws1", Name: "m-bob"},
		directory.Membership{
			Subject: tenancyv1alpha1.Subject{Kind: tenancyv1alpha1.SubjectKindUser, Name: "bob"},
			Role:    "view", Tenant: "acme", Project: "web",
		})
	d.SetReady()
	return d
}

func ctxWithUser(name string) context.Context {
	return genericapirequest.WithUser(context.Background(), &user.DefaultInfo{Name: name})
}

// recorder captures what the storage asked to have created.
type recorder struct {
	cluster string
	project *tenancyv1alpha1.Project
	created bool
	calls   int
}

func (r *recorder) create(_ context.Context, cluster string, p *tenancyv1alpha1.Project) (bool, error) {
	r.calls++
	r.cluster, r.project = cluster, p
	return r.created, nil
}

func request(tenant, name string) *tenancyv1alpha1.SelfProjectRequest {
	return &tenancyv1alpha1.SelfProjectRequest{
		Spec: tenancyv1alpha1.SelfProjectRequestSpec{Tenant: tenant, Name: name},
	}
}

func TestAdminCreatesInTheTenantWorkspace(t *testing.T) {
	rec := &recorder{created: true}
	r := NewREST(readyDirectory(), rec.create)

	obj, err := r.Create(ctxWithUser("alice"), request("acme", "analytics"), nil, nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// The cluster comes from the directory, never from the request: a
	// caller cannot aim this credential at a workspace of their choosing.
	if rec.cluster != "ws1" {
		t.Errorf("created in cluster %q, want the tenant's own ws1", rec.cluster)
	}
	if rec.project.Name != "analytics" || rec.project.Spec.Tenant != "acme" {
		t.Errorf("created %+v", rec.project)
	}
	// An unset display name should not reach the object empty.
	if rec.project.Spec.DisplayName != "analytics" {
		t.Errorf("displayName = %q, want it defaulted to the name", rec.project.Spec.DisplayName)
	}

	out := obj.(*tenancyv1alpha1.SelfProjectRequest)
	if !out.Status.Created || out.Status.Project != "analytics" {
		t.Errorf("status = %+v", out.Status)
	}
}

func TestExistingProjectIsNotAnError(t *testing.T) {
	// Asking for what already exists is the same request, answered.
	rec := &recorder{created: false}
	r := NewREST(readyDirectory(), rec.create)

	obj, err := r.Create(ctxWithUser("alice"), request("acme", "web"), nil, nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if out := obj.(*tenancyv1alpha1.SelfProjectRequest); out.Status.Created {
		t.Errorf("status says created, want created=false for an existing project")
	}
}

func TestNonAdminMemberIsRefused(t *testing.T) {
	rec := &recorder{}
	r := NewREST(readyDirectory(), rec.create)

	_, err := r.Create(ctxWithUser("bob"), request("acme", "analytics"), nil, nil)
	if !apierrors.IsForbidden(err) {
		t.Fatalf("err = %v, want Forbidden", err)
	}
	if rec.calls != 0 {
		t.Errorf("the credential was used %d times for a refused request", rec.calls)
	}
}

func TestStrangerIsRefused(t *testing.T) {
	rec := &recorder{}
	r := NewREST(readyDirectory(), rec.create)

	_, err := r.Create(ctxWithUser("mallory"), request("acme", "analytics"), nil, nil)
	if !apierrors.IsForbidden(err) {
		t.Fatalf("err = %v, want Forbidden", err)
	}
	// The refusal must not say whether the tenant exists: to a
	// non-member, "no such tenant" and "not your tenant" are the same.
	if got := err.Error(); !contains(got, "not an admin") {
		t.Errorf("message %q reveals more than membership", got)
	}
	if rec.calls != 0 {
		t.Errorf("the credential was used for a stranger's request")
	}
}

func TestUnknownTenantLooksLikeNonMembership(t *testing.T) {
	rec := &recorder{}
	r := NewREST(readyDirectory(), rec.create)

	_, err := r.Create(ctxWithUser("alice"), request("no-such-tenant", "x"), nil, nil)
	if !apierrors.IsForbidden(err) {
		t.Fatalf("err = %v, want Forbidden rather than NotFound", err)
	}
}

func TestNameMustBeAWorkspaceName(t *testing.T) {
	rec := &recorder{}
	r := NewREST(readyDirectory(), rec.create)

	// The name seeds a workspace name, so it has to be a DNS label.
	_, err := r.Create(ctxWithUser("alice"), request("acme", "Not A Label"), nil, nil)
	if !apierrors.IsInvalid(err) {
		t.Fatalf("err = %v, want Invalid", err)
	}
	if rec.calls != 0 {
		t.Errorf("an invalid request reached the credential")
	}
}

func TestUnconfiguredCreationIsHonest(t *testing.T) {
	// A server without the writer should say so rather than refuse as if
	// it were an authorization decision.
	r := NewREST(readyDirectory(), nil)

	_, err := r.Create(ctxWithUser("alice"), request("acme", "analytics"), nil, nil)
	if !apierrors.IsServiceUnavailable(err) {
		t.Fatalf("err = %v, want ServiceUnavailable", err)
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (haystack == needle ||
		len(needle) == 0 ||
		indexOf(haystack, needle) >= 0)
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
