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
	"reflect"
	"testing"

	tenancyv1alpha1 "github.com/kcp-dev/contrib-virtual-workspaces/tenancy/sdk/apis/tenancy/v1alpha1"
)

func endpoint(cluster string) string { return "https://kcp.example.com/clusters/" + cluster }

func user(name string) tenancyv1alpha1.Subject {
	return tenancyv1alpha1.Subject{Kind: tenancyv1alpha1.SubjectKindUser, Name: name}
}

func group(name string) tenancyv1alpha1.Subject {
	return tenancyv1alpha1.Subject{Kind: tenancyv1alpha1.SubjectKindGroup, Name: name}
}

// A directory shaped like the real tiers: the Tenant lives in the platform
// cluster, its Projects and Memberships inside the tenant's OWN workspace
// cluster (ws-acme), and each project has a workspace of its own.
func fixture() *Directory {
	d := New()
	d.UpsertTenant(Key{"platform", "acme"}, Tenant{DisplayName: "Acme", WorkspaceCluster: "ws-acme"})
	d.UpsertProject(Key{"ws-acme", "web"}, Project{Tenant: "acme", DisplayName: "Web", WorkspaceCluster: "ws-web"})
	d.UpsertProject(Key{"ws-acme", "api"}, Project{Tenant: "acme", DisplayName: "API", WorkspaceCluster: "ws-api"})
	return d
}

func TestTenantWideRoleReachesEveryProject(t *testing.T) {
	d := fixture()
	d.UpsertMembership(Key{"ws-acme", "m1"}, Membership{Subject: user("alice"), Role: "edit", Tenant: "acme"})

	claims := d.ReviewFor("alice", nil, endpoint)
	if len(claims) != 1 {
		t.Fatalf("expected 1 tenant claim, got %d", len(claims))
	}
	c := claims[0]
	if c.Name != "acme" || c.Cluster != "ws-acme" || c.Endpoint != endpoint("ws-acme") {
		t.Errorf("unexpected tenant claim: %+v", c)
	}
	if !reflect.DeepEqual(c.Roles, []string{"edit"}) {
		t.Errorf("tenant roles = %v, want [edit]", c.Roles)
	}
	if len(c.Projects) != 2 || c.Projects[0].Name != "api" || c.Projects[1].Name != "web" {
		t.Fatalf("expected both projects sorted by name, got %+v", c.Projects)
	}
	for _, p := range c.Projects {
		if !reflect.DeepEqual(p.Roles, []string{"edit"}) {
			t.Errorf("project %s roles = %v, want [edit]", p.Name, p.Roles)
		}
	}
}

func TestProjectScopedGrantReachesOneProject(t *testing.T) {
	d := fixture()
	d.UpsertMembership(Key{"ws-acme", "m1"}, Membership{Subject: user("bob"), Role: "view", Tenant: "acme", Project: "web"})

	claims := d.ReviewFor("bob", nil, endpoint)
	if len(claims) != 1 {
		t.Fatalf("expected 1 tenant claim, got %d", len(claims))
	}
	c := claims[0]
	if len(c.Roles) != 0 {
		t.Errorf("expected no tenant-wide roles, got %v", c.Roles)
	}
	if len(c.Projects) != 1 || c.Projects[0].Name != "web" {
		t.Fatalf("expected only project web, got %+v", c.Projects)
	}
}

func TestGroupAndUserGrantsUnion(t *testing.T) {
	d := fixture()
	d.UpsertMembership(Key{"ws-acme", "m1"}, Membership{Subject: group("devs"), Role: "view", Tenant: "acme"})
	d.UpsertMembership(Key{"ws-acme", "m2"}, Membership{Subject: user("carol"), Role: "admin", Tenant: "acme", Project: "web"})

	claims := d.ReviewFor("carol", []string{"devs"}, endpoint)
	if len(claims) != 1 {
		t.Fatalf("expected 1 tenant claim, got %d", len(claims))
	}
	c := claims[0]
	if !reflect.DeepEqual(c.Roles, []string{"view"}) {
		t.Errorf("tenant roles = %v, want [view]", c.Roles)
	}
	var web *tenancyv1alpha1.ProjectClaim
	for i := range c.Projects {
		if c.Projects[i].Name == "web" {
			web = &c.Projects[i]
		}
	}
	if web == nil {
		t.Fatalf("expected project web in %+v", c.Projects)
	}
	if !reflect.DeepEqual(web.Roles, []string{"admin", "view"}) {
		t.Errorf("web roles = %v, want [admin view]", web.Roles)
	}
}

func TestIdentitiesAreIsolated(t *testing.T) {
	d := fixture()
	d.UpsertMembership(Key{"ws-acme", "m1"}, Membership{Subject: user("alice"), Role: "admin", Tenant: "acme"})

	if claims := d.ReviewFor("mallory", []string{"other"}, endpoint); len(claims) != 0 {
		t.Errorf("mallory should see nothing, got %+v", claims)
	}
}

func TestRemovalRevokes(t *testing.T) {
	d := fixture()
	k := Key{"ws-acme", "m1"}
	d.UpsertMembership(k, Membership{Subject: user("alice"), Role: "admin", Tenant: "acme"})
	d.RemoveMembership(k)

	if claims := d.ReviewFor("alice", nil, endpoint); len(claims) != 0 {
		t.Errorf("expected no claims after removal, got %+v", claims)
	}
}

func TestMembershipInAnUnknownWorkspaceClaimsNothing(t *testing.T) {
	d := New()
	d.UpsertMembership(Key{"ws-acme", "m1"}, Membership{Subject: user("alice"), Role: "admin", Tenant: "ghost"})

	if claims := d.ReviewFor("alice", nil, endpoint); len(claims) != 0 {
		t.Errorf("expected no claims for a workspace whose Tenant the directory has not seen, got %+v", claims)
	}
}

func TestForgetClusterDropsEverything(t *testing.T) {
	d := fixture()
	d.UpsertMembership(Key{"ws-acme", "m1"}, Membership{Subject: user("alice"), Role: "admin", Tenant: "acme"})
	// The platform cluster going away takes the Tenant with it, and with it
	// every claim that resolved through it.
	d.ForgetCluster("platform")
	d.ForgetCluster("ws-acme")

	if claims := d.ReviewFor("alice", nil, endpoint); len(claims) != 0 {
		t.Errorf("expected no claims after ForgetCluster, got %+v", claims)
	}
	tn, pr, ms := d.Counts()
	if tn+pr+ms != 0 {
		t.Errorf("expected empty directory, have %d/%d/%d", tn, pr, ms)
	}
}

func TestSameTenantNameInTwoPlatformsStaysSeparate(t *testing.T) {
	d := New()
	d.UpsertTenant(Key{"platform1", "acme"}, Tenant{DisplayName: "Acme One", WorkspaceCluster: "ws-1"})
	d.UpsertTenant(Key{"platform2", "acme"}, Tenant{DisplayName: "Acme Two", WorkspaceCluster: "ws-2"})
	// The grant lives in the first tenant's workspace, so it can only
	// resolve to that tenant however many share the name.
	d.UpsertMembership(Key{"ws-1", "m1"}, Membership{Subject: user("alice"), Role: "view", Tenant: "acme"})

	claims := d.ReviewFor("alice", nil, endpoint)
	if len(claims) != 1 || claims[0].Cluster != "ws-1" {
		t.Errorf("expected only the first tenant, got %+v", claims)
	}
	if claims[0].DisplayName != "Acme One" {
		t.Errorf("resolved the wrong Tenant object: %+v", claims[0])
	}
}
