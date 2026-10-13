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

package selftenancyreview

import (
	"context"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apiserver/pkg/authentication/user"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"

	"github.com/kcp-dev/contrib-virtual-workspaces/tenancy/pkg/directory"
	tenancyv1alpha1 "github.com/kcp-dev/contrib-virtual-workspaces/tenancy/sdk/apis/tenancy/v1alpha1"
)

func endpoint(cluster string) string { return "https://example.test/clusters/" + cluster }

// The Tenant lives in the platform workspace; the Membership lives inside
// the tenant's own workspace (ws1), which is how the two are joined.
func readyDirectory() *directory.Directory {
	d := directory.New()
	d.UpsertTenant(directory.Key{Cluster: "platform", Name: "acme"},
		directory.Tenant{DisplayName: "Acme", WorkspaceCluster: "ws1"})
	d.UpsertMembership(directory.Key{Cluster: "ws1", Name: "m1"},
		directory.Membership{
			Subject: tenancyv1alpha1.Subject{Kind: tenancyv1alpha1.SubjectKindUser, Name: "alice"},
			Role:    "admin", Tenant: "acme",
		})
	d.SetReady()
	return d
}

func ctxWithUser(name string, groups ...string) context.Context {
	return genericapirequest.WithUser(context.Background(), &user.DefaultInfo{Name: name, Groups: groups})
}

func TestCreateAnswersForTheCaller(t *testing.T) {
	r := NewREST(readyDirectory(), endpoint)

	obj, err := r.Create(ctxWithUser("alice"), &tenancyv1alpha1.SelfTenancyReview{}, nil, nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	review := obj.(*tenancyv1alpha1.SelfTenancyReview)
	if len(review.Status.Tenants) != 1 || review.Status.Tenants[0].Name != "acme" {
		t.Fatalf("unexpected answer: %+v", review.Status)
	}
	if review.Status.Tenants[0].Endpoint != endpoint("ws1") {
		t.Errorf("endpoint = %q", review.Status.Tenants[0].Endpoint)
	}
}

func TestCreateAnswersEmptyForStrangers(t *testing.T) {
	r := NewREST(readyDirectory(), endpoint)

	obj, err := r.Create(ctxWithUser("mallory"), &tenancyv1alpha1.SelfTenancyReview{}, nil, nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	review := obj.(*tenancyv1alpha1.SelfTenancyReview)
	if len(review.Status.Tenants) != 0 {
		t.Errorf("mallory should get an empty answer, got %+v", review.Status.Tenants)
	}
}

func TestCreateRefusesWithoutIdentity(t *testing.T) {
	r := NewREST(readyDirectory(), endpoint)

	_, err := r.Create(context.Background(), &tenancyv1alpha1.SelfTenancyReview{}, nil, nil)
	if !apierrors.IsUnauthorized(err) {
		t.Errorf("expected Unauthorized, got %v", err)
	}
}

func TestCreateRefusesBeforeSync(t *testing.T) {
	r := NewREST(directory.New(), endpoint)

	_, err := r.Create(ctxWithUser("alice"), &tenancyv1alpha1.SelfTenancyReview{}, nil, nil)
	if !apierrors.IsServiceUnavailable(err) {
		t.Errorf("expected ServiceUnavailable, got %v", err)
	}
}

func TestCreateRejectsWrongType(t *testing.T) {
	r := NewREST(readyDirectory(), endpoint)

	_, err := r.Create(ctxWithUser("alice"), &tenancyv1alpha1.Tenant{}, nil, nil)
	if !apierrors.IsBadRequest(err) {
		t.Errorf("expected BadRequest, got %v", err)
	}
}
