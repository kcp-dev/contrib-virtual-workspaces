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
	"context"
	"slices"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kcptenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"

	tenancyv1alpha1 "github.com/kcp-dev/contrib-virtual-workspaces/tenancy/sdk/apis/tenancy/v1alpha1"
)

// The harness ran `tenancy-vw init` twice before any test, so this only
// has to assert the result, and that the second run did not corrupt it.
func TestScenarioInstallIsIdempotent(t *testing.T) {
	ctx := testContext(t)

	// The export and its schemas exist in the controllers workspace.
	dyn := dynamicFor(t, exportPath)
	export, err := dyn.Resource(apiExportGVR).Get(ctx, "tenancy.contrib.kcp.io", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("APIExport missing after double init: %v", err)
	}
	resources, _, _ := unstructured.NestedSlice(export.Object, "spec", "resources")
	if len(resources) != 3 {
		t.Errorf("APIExport should export 3 resources, has %d", len(resources))
	}
	for _, name := range []string{
		"v1alpha1.tenants.tenancy.contrib.kcp.io",
		"v1alpha1.projects.tenancy.contrib.kcp.io",
		"v1alpha1.memberships.tenancy.contrib.kcp.io",
	} {
		if _, err := dyn.Resource(apiResourceSchemaGVR).Get(ctx, name, metav1.GetOptions{}); err != nil {
			t.Errorf("APIResourceSchema %s missing: %v", name, err)
		}
	}

	// And the strongest signal: an organization can bind and use the API.
	orgPath := organization(t, ctx)
	org := adminClient(t, orgPath)
	var tenants tenancyv1alpha1.TenantList
	if err := org.List(ctx, &tenants); err != nil {
		t.Fatalf("tenancy API not usable in %s after double init: %v", orgPath, err)
	}
}

func TestScenarioTenantGetsAUsableWorkspace(t *testing.T) {
	ctx := testContext(t)
	orgPath := organization(t, ctx)
	org := adminClient(t, orgPath)

	tenant := createTenant(t, ctx, org, "acme", "Acme Corp")

	if tenant.Status.Workspace != "acme-corp" {
		t.Errorf("slug strategy should name the workspace acme-corp, got %q", tenant.Status.Workspace)
	}

	// The workspace is real and addressable: a read at the reported URL
	// serves.
	ws := workspaceClient(t, tenant.Status.URL)
	var crbs rbacv1.ClusterRoleBindingList
	if err := ws.List(ctx, &crbs); err != nil {
		t.Fatalf("tenant workspace at %s is not usable: %v", tenant.Status.URL, err)
	}
}

func TestScenarioDisplayNameCollisionsGetDistinctWorkspaces(t *testing.T) {
	ctx := testContext(t)
	orgPath := organization(t, ctx)
	org := adminClient(t, orgPath)

	first := createTenant(t, ctx, org, "metal-one", "Heavy Metal")
	second := createTenant(t, ctx, org, "metal-two", "Heavy Metal")

	if first.Status.Workspace == second.Status.Workspace {
		t.Fatalf("both tenants got workspace %q", first.Status.Workspace)
	}
	if first.Status.Workspace != "heavy-metal" {
		t.Errorf("first tenant should win the pretty name, got %q", first.Status.Workspace)
	}
	if second.Status.WorkspaceCluster == first.Status.WorkspaceCluster {
		t.Errorf("tenants share a logical cluster: %q", first.Status.WorkspaceCluster)
	}
}

func TestScenarioHostileDisplayNamesStillProvision(t *testing.T) {
	ctx := testContext(t)
	orgPath := organization(t, ctx)
	org := adminClient(t, orgPath)

	for i, hostile := range []string{"🔥🔥🔥", "---", "ÜBER Größe", "a b c d e f g h i j k l m n o p q r s t u v w x y z and then some more to overflow"} {
		tenant := createTenant(t, ctx, org, "hostile-"+randomSuffix(t), hostile)
		if tenant.Status.Workspace == "" {
			t.Errorf("case %d (%q): no workspace", i, hostile)
		}
	}
}

func TestScenarioProjectNestsUnderTenant(t *testing.T) {
	ctx := testContext(t)
	orgPath := organization(t, ctx)
	org := adminClient(t, orgPath)

	tenant := createTenant(t, ctx, org, "acme", "Acme Corp")
	project := createProject(t, ctx, org, "web", "acme", "Web Shop")

	// The project's Workspace object lives inside the tenant's workspace.
	tenantWS := workspaceClient(t, tenant.Status.URL)
	var ws kcptenancyv1alpha1.Workspace
	if err := tenantWS.Get(ctx, client.ObjectKey{Name: project.Status.Workspace}, &ws); err != nil {
		t.Fatalf("project workspace %q not found under the tenant: %v", project.Status.Workspace, err)
	}
	if project.Status.WorkspaceCluster == tenant.Status.WorkspaceCluster {
		t.Errorf("project shares the tenant's logical cluster %q", tenant.Status.WorkspaceCluster)
	}
}

func TestScenarioMembershipMaterializesAndRevokes(t *testing.T) {
	ctx := testContext(t)
	orgPath := organization(t, ctx)
	org := adminClient(t, orgPath)

	tenant := createTenant(t, ctx, org, "acme", "Acme Corp")
	membership := createMembership(t, ctx, org, "alice-admin", tenancyv1alpha1.MembershipSpec{
		Subject: tenancyv1alpha1.Subject{Kind: tenancyv1alpha1.SubjectKindUser, Name: "alice"},
		Role:    tenancyv1alpha1.RoleAdmin,
		Tenant:  "acme",
	})

	ws := workspaceClient(t, tenant.Status.URL)
	crb, err := getBinding(ctx, ws, membership.Name)
	if err != nil {
		t.Fatalf("materialized binding not found: %v", err)
	}
	if len(crb.Subjects) != 1 || crb.Subjects[0].Name != "alice" || crb.Subjects[0].Kind != "User" {
		t.Errorf("binding subjects = %+v", crb.Subjects)
	}
	if crb.RoleRef.Name != "tenancy.contrib.kcp.io:role:admin" {
		t.Errorf("binding roleRef = %q", crb.RoleRef.Name)
	}

	// Revoke: deleting the membership removes the binding again.
	if err := org.Delete(ctx, membership); err != nil {
		t.Fatalf("delete membership: %v", err)
	}
	waitFor(t, ctx, "binding removed after revoke", func(ctx context.Context) (bool, error) {
		_, err := getBinding(ctx, ws, membership.Name)
		return apierrors.IsNotFound(err), nil
	})
}

func TestScenarioChangingTheRoleReplacesTheBinding(t *testing.T) {
	ctx := testContext(t)
	orgPath := organization(t, ctx)
	org := adminClient(t, orgPath)

	tenant := createTenant(t, ctx, org, "acme", "Acme Corp")
	membership := createMembership(t, ctx, org, "bob-view", tenancyv1alpha1.MembershipSpec{
		Subject: tenancyv1alpha1.Subject{Kind: tenancyv1alpha1.SubjectKindUser, Name: "bob"},
		Role:    tenancyv1alpha1.RoleView,
		Tenant:  "acme",
	})

	membership.Spec.Role = tenancyv1alpha1.RoleEdit
	if err := org.Update(ctx, membership); err != nil {
		t.Fatalf("update membership role: %v", err)
	}

	ws := workspaceClient(t, tenant.Status.URL)
	waitFor(t, ctx, "binding re-pointed at the edit role", func(ctx context.Context) (bool, error) {
		crb, err := getBinding(ctx, ws, membership.Name)
		if err != nil {
			return false, nil
		}
		return crb.RoleRef.Name == "tenancy.contrib.kcp.io:role:edit", nil
	})
}

func TestScenarioSelfTenancyReviewAnswersOnlyTheCaller(t *testing.T) {
	ctx := testContext(t)
	orgPath := organization(t, ctx)
	org := adminClient(t, orgPath)

	tenant := createTenant(t, ctx, org, "acme", "Acme Corp")
	createProject(t, ctx, org, "web", "acme", "Web Shop")
	createMembership(t, ctx, org, "alice-admin", tenancyv1alpha1.MembershipSpec{
		Subject: tenancyv1alpha1.Subject{Kind: tenancyv1alpha1.SubjectKindUser, Name: "alice"},
		Role:    tenancyv1alpha1.RoleAdmin,
		Tenant:  "acme",
	})

	review := selfTenancyReview(t, ctx, "alice", "alice sees acme", func(r *tenancyv1alpha1.SelfTenancyReview) bool {
		for _, claim := range r.Status.Tenants {
			if claim.Name == "acme" && slices.Contains(claim.Roles, "admin") && len(claim.Projects) == 1 {
				return true
			}
		}
		return false
	})

	var acme *tenancyv1alpha1.TenantClaim
	for i := range review.Status.Tenants {
		if review.Status.Tenants[i].Name == "acme" && review.Status.Tenants[i].Cluster == tenant.Status.WorkspaceCluster {
			acme = &review.Status.Tenants[i]
		}
	}
	if acme == nil {
		t.Fatalf("alice's review has no claim for this org's acme: %+v", review.Status.Tenants)
	}
	if acme.Endpoint == "" || acme.DisplayName != "Acme Corp" {
		t.Errorf("claim is incomplete: %+v", acme)
	}
	if acme.Projects[0].Name != "web" || !slices.Contains(acme.Projects[0].Roles, "admin") {
		t.Errorf("tenant-wide admin should reach the project: %+v", acme.Projects)
	}

	// A stranger gets an empty answer, not an error and not someone
	// else's tenants.
	stranger := selfTenancyReview(t, ctx, "mallory", "mallory sees nothing for this org", func(r *tenancyv1alpha1.SelfTenancyReview) bool {
		for _, claim := range r.Status.Tenants {
			if claim.Cluster == tenant.Status.WorkspaceCluster {
				return false
			}
		}
		return true
	})
	_ = stranger
}

func TestScenarioGroupMembershipReachesGroupMembers(t *testing.T) {
	ctx := testContext(t)
	orgPath := organization(t, ctx)
	org := adminClient(t, orgPath)

	tenant := createTenant(t, ctx, org, "acme", "Acme Corp")
	createMembership(t, ctx, org, "team-a-edit", tenancyv1alpha1.MembershipSpec{
		Subject: tenancyv1alpha1.Subject{Kind: tenancyv1alpha1.SubjectKindGroup, Name: "team-a"},
		Role:    tenancyv1alpha1.RoleEdit,
		Tenant:  "acme",
	})

	// alice's harness certificate carries O=team-a; bob's carries O=team-b.
	selfTenancyReview(t, ctx, "alice", "alice reaches acme through team-a", func(r *tenancyv1alpha1.SelfTenancyReview) bool {
		for _, claim := range r.Status.Tenants {
			if claim.Cluster == tenant.Status.WorkspaceCluster && slices.Contains(claim.Roles, "edit") {
				return true
			}
		}
		return false
	})
	selfTenancyReview(t, ctx, "bob", "bob does not reach acme", func(r *tenancyv1alpha1.SelfTenancyReview) bool {
		for _, claim := range r.Status.Tenants {
			if claim.Cluster == tenant.Status.WorkspaceCluster {
				return false
			}
		}
		return true
	})
}

func TestScenarioTenantDeletionRemovesTheWorkspace(t *testing.T) {
	ctx := testContext(t)
	orgPath := organization(t, ctx)
	org := adminClient(t, orgPath)

	tenant := createTenant(t, ctx, org, "doomed", "Doomed Tenant")
	workspaceName := tenant.Status.Workspace

	if err := org.Delete(ctx, tenant); err != nil {
		t.Fatalf("delete tenant: %v", err)
	}

	waitFor(t, ctx, "tenant object gone", func(ctx context.Context) (bool, error) {
		err := org.Get(ctx, client.ObjectKey{Name: "doomed"}, &tenancyv1alpha1.Tenant{})
		return apierrors.IsNotFound(err), nil
	})
	waitFor(t, ctx, "tenant workspace gone", func(ctx context.Context) (bool, error) {
		err := org.Get(ctx, client.ObjectKey{Name: workspaceName}, &kcptenancyv1alpha1.Workspace{})
		return apierrors.IsNotFound(err), nil
	})
}
