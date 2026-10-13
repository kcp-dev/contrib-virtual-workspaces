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

package selftenantrequest

import (
	"context"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apiserver/pkg/authentication/user"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"

	tenancyv1alpha1 "github.com/kcp-dev/contrib-virtual-workspaces/tenancy/sdk/apis/tenancy/v1alpha1"
)

// This is the one endpoint with no Membership to authorize against, so
// the tests are about the gate and about what the caller cannot choose.

type recorder struct {
	tenant  *tenancyv1alpha1.Tenant
	created bool
	calls   int
}

func (r *recorder) create(_ context.Context, t *tenancyv1alpha1.Tenant) (bool, error) {
	r.calls++
	r.tenant = t
	return r.created, nil
}

func ctxWithUser(name string, groups ...string) context.Context {
	return genericapirequest.WithUser(context.Background(), &user.DefaultInfo{Name: name, Groups: groups})
}

func request(name string) *tenancyv1alpha1.SelfTenantRequest {
	return &tenancyv1alpha1.SelfTenantRequest{
		Spec: tenancyv1alpha1.SelfTenantRequestSpec{Name: name},
	}
}

func TestDisabledByDefault(t *testing.T) {
	rec := &recorder{}
	r := NewREST(Options{}, rec.create)

	_, err := r.Create(ctxWithUser("alice@example.com"), request("acme"), nil, nil)
	if !apierrors.IsServiceUnavailable(err) {
		t.Fatalf("err = %v, want ServiceUnavailable", err)
	}
	if rec.calls != 0 {
		t.Errorf("a tenant was created while self-service was off")
	}
}

func TestEnabledCallerBecomesFirstAdmin(t *testing.T) {
	rec := &recorder{created: true}
	r := NewREST(Options{Enabled: true}, rec.create)

	obj, err := r.Create(ctxWithUser("alice@example.com"), request("acme"), nil, nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// A tenant nobody administers is unusable, so the caller must come out
	// of this as its admin.
	if rec.tenant.Spec.FirstAdmin != "alice@example.com" {
		t.Errorf("firstAdmin = %q, want the caller", rec.tenant.Spec.FirstAdmin)
	}
	if rec.tenant.Spec.DisplayName != "acme" {
		t.Errorf("displayName = %q, want it defaulted to the name", rec.tenant.Spec.DisplayName)
	}
	if out := obj.(*tenancyv1alpha1.SelfTenantRequest); !out.Status.Created {
		t.Errorf("status = %+v", out.Status)
	}
}

func TestCallerCannotNameSomeoneElseAsAdmin(t *testing.T) {
	rec := &recorder{created: true}
	r := NewREST(Options{Enabled: true}, rec.create)

	// There is no field for it, so the only way to try is to smuggle one
	// in — and the server overwrites whatever arrived.
	req := request("acme")
	_, err := r.Create(ctxWithUser("mallory@example.com"), req, nil, nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if rec.tenant.Spec.FirstAdmin != "mallory@example.com" {
		t.Errorf("firstAdmin = %q, want the authenticated caller", rec.tenant.Spec.FirstAdmin)
	}
}

func TestGroupRestriction(t *testing.T) {
	rec := &recorder{created: true}
	r := NewREST(Options{Enabled: true, Groups: []string{"staff"}}, rec.create)

	if _, err := r.Create(ctxWithUser("alice@example.com", "staff"), request("acme"), nil, nil); err != nil {
		t.Fatalf("a caller in the group was refused: %v", err)
	}

	_, err := r.Create(ctxWithUser("mallory@example.com", "others"), request("evil"), nil, nil)
	if !apierrors.IsForbidden(err) {
		t.Fatalf("err = %v, want Forbidden for a caller outside the groups", err)
	}
	if rec.calls != 1 {
		t.Errorf("calls = %d, want only the permitted one", rec.calls)
	}
}

func TestExistingTenantIsNotClaimed(t *testing.T) {
	// Someone else may already hold that name. The answer must not imply
	// the caller now administers it.
	rec := &recorder{created: false}
	r := NewREST(Options{Enabled: true}, rec.create)

	obj, err := r.Create(ctxWithUser("mallory@example.com"), request("acme"), nil, nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	out := obj.(*tenancyv1alpha1.SelfTenantRequest)
	if out.Status.Created {
		t.Errorf("status claims creation of an existing tenant")
	}
	if got := out.Status.Message; got != `tenant "acme" already exists` {
		t.Errorf("message = %q, should not promise admin of someone else's tenant", got)
	}
}

func TestNameMustBeAWorkspaceName(t *testing.T) {
	rec := &recorder{}
	r := NewREST(Options{Enabled: true}, rec.create)

	_, err := r.Create(ctxWithUser("alice@example.com"), request("Not A Label"), nil, nil)
	if !apierrors.IsInvalid(err) {
		t.Fatalf("err = %v, want Invalid", err)
	}
	if rec.calls != 0 {
		t.Errorf("an invalid request reached the credential")
	}
}

func TestUnauthenticatedIsRefused(t *testing.T) {
	rec := &recorder{}
	r := NewREST(Options{Enabled: true}, rec.create)

	_, err := r.Create(context.Background(), request("acme"), nil, nil)
	if !apierrors.IsUnauthorized(err) {
		t.Fatalf("err = %v, want Unauthorized", err)
	}
}
