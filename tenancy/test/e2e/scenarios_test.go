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

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kcptenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"

	tenancyv1alpha1 "github.com/kcp-dev/contrib-virtual-workspaces/tenancy/sdk/apis/tenancy/v1alpha1"
)

// The harness ran `tenancy-vw init` twice before any test, so this asserts
// the result and that the second run did not corrupt it.
func TestScenarioInstallIsIdempotent(t *testing.T) {
	ctx := testContext(t)
	dyn := dynamicFor(t, exportsPath)

	// Four exports, two of which deliberately declare no resources at all:
	// binding those grants a capability and adds no tenant-visible API.
	wantResources := map[string]int{
		exportPlatform:    1, // tenants
		exportTenancy:     2, // projects, memberships
		exportProvisioner: 0,
		exportAccess:      0,
	}
	for name, want := range wantResources {
		export, err := dyn.Resource(apiExportGVR).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("APIExport %s missing after double init: %v", name, err)
		}
		resources, _, _ := unstructured.NestedSlice(export.Object, "spec", "resources")
		if len(resources) != want {
			t.Errorf("APIExport %s exports %d resources, want %d", name, len(resources), want)
		}
	}

	// The capability exports carry claims; that is their whole content.
	for _, name := range []string{exportProvisioner, exportAccess} {
		export, err := dyn.Resource(apiExportGVR).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("get %s: %v", name, err)
		}
		claims, _, _ := unstructured.NestedSlice(export.Object, "spec", "permissionClaims")
		if len(claims) == 0 {
			t.Errorf("APIExport %s declares neither resources nor claims, so it grants nothing", name)
		}
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

	for _, name := range []string{"tenant", "project"} {
		if _, err := dyn.Resource(workspaceTypeGVR).Get(ctx, name, metav1.GetOptions{}); err != nil {
			t.Errorf("WorkspaceType %s missing: %v", name, err)
		}
	}
}

// The two hand-bound tiers, and the fact that neither can do the other's
// job. The store keeps the records; the tenants workspace is where the
// workspaces go. Splitting them means a bug in the provisioning path
// cannot rewrite the registry that drives it.
func TestScenarioInitBindsTheTwoHandBoundTiers(t *testing.T) {
	ctx := testContext(t)

	store := bindingNames(t, ctx, dynamicFor(t, storePath))
	if !hasBinding(store, exportPlatform) {
		t.Errorf("the store is not bound to %s: %v", exportPlatform, store)
	}
	if hasBinding(store, exportProvisioner) {
		t.Errorf("the store is bound to %s; no workspace is created here, so the capability "+
			"to create one has no business being granted here: %v", exportProvisioner, store)
	}
	if hasBinding(store, exportAccess) {
		t.Errorf("the store is bound to %s; nothing should grant RBAC at this tier: %v",
			exportAccess, store)
	}

	parent := bindingNames(t, ctx, dynamicFor(t, tenantsPath))
	if !hasBinding(parent, exportProvisioner) {
		t.Errorf("%s is not bound to %s, so tenant workspaces cannot be created: %v",
			tenantsPath, exportProvisioner, parent)
	}
	if hasBinding(parent, exportPlatform) {
		t.Errorf("%s can see the Tenant records it provisions from: %v", tenantsPath, parent)
	}

	// And the Tenant API is servable in the store, not the parent.
	var tenants tenancyv1alpha1.TenantList
	if err := storeClient(t).List(ctx, &tenants); err != nil {
		t.Fatalf("the Tenant API is not usable in %s: %v", storePath, err)
	}
}

func TestScenarioTenantGetsAUsableWorkspace(t *testing.T) {
	ctx := testContext(t)
	name := "acme-" + randomSuffix(t)

	tenant := createTenant(t, ctx, name, "Acme Corp")

	// The slug strategy names it after the display name; a collision with
	// another tenant of the same display name falls back to a UID suffix.
	if !hasPrefix(tenant.Status.Workspace, "acme-corp") {
		t.Errorf("unexpected workspace name %q for display name %q", tenant.Status.Workspace, "Acme Corp")
	}

	// The workspace is real, of the `tenant` type, and lives under the
	// provisioning parent rather than beside its own record.
	parentDyn := dynamicFor(t, tenantsPath)
	ws, err := parentDyn.Resource(workspaceGVR).Get(ctx, tenant.Status.Workspace, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get the provisioned Workspace: %v", err)
	}
	typeName, _, _ := unstructured.NestedString(ws.Object, "spec", "type", "name")
	if typeName != "tenant" {
		t.Errorf("tenant workspace has type %q, want tenant — nothing would bind it", typeName)
	}
}

// The tenant tier carries the two capabilities it needs and, crucially, not
// the one that would let anything grant access to it.
func TestScenarioTenantWorkspaceCarriesTheRightBindings(t *testing.T) {
	ctx := testContext(t)
	tenant := createTenant(t, ctx, "bind-"+randomSuffix(t), "Binding Corp")

	names := bindingNames(t, ctx, dynamicForURL(t, tenant.Status.URL))
	if !hasBinding(names, exportTenancy) {
		t.Errorf("tenant workspace is not bound to %s, so Projects and Memberships are not servable: %v",
			exportTenancy, names)
	}
	if !hasBinding(names, exportProvisioner) {
		t.Errorf("tenant workspace is not bound to %s, so project workspaces cannot be created: %v",
			exportProvisioner, names)
	}
	if hasBinding(names, exportAccess) {
		t.Errorf("tenant workspace is bound to %s. That export carries the RBAC claims, and binding "+
			"it here would let a tenant be granted access to the tier that decides who reaches what: %v",
			exportAccess, names)
	}
}

// A project workspace carries exactly one binding, and it adds no
// tenant-facing API — which is what makes the operator's reach declared and
// auditable instead of ambient.
func TestScenarioProjectWorkspaceCarriesOnlyTheAccessBinding(t *testing.T) {
	ctx := testContext(t)

	tenantName := "proj-" + randomSuffix(t)
	tenant := createTenant(t, ctx, tenantName, "Project Corp")
	tenantWS := workspaceClient(t, tenant.Status.URL)
	project := createProject(t, ctx, tenantWS, "web", tenantName, "Web Shop")

	projectDyn := dynamicForURL(t, project.Status.URL)
	names := bindingNames(t, ctx, projectDyn)
	if !hasBinding(names, exportAccess) {
		t.Fatalf("project workspace is not bound to %s, so the operator cannot write RBAC there: %v",
			exportAccess, names)
	}
	if hasBinding(names, exportProvisioner) {
		t.Errorf("project workspace is bound to %s; nothing should be able to create workspaces inside a project: %v",
			exportProvisioner, names)
	}
	if hasBinding(names, exportTenancy) || hasBinding(names, exportPlatform) {
		t.Errorf("project workspace can see the tenancy model itself: %v", names)
	}

	// The `project` type omits extend: root:universal, so kcp does not
	// create `default` and the operator must — through the namespaces claim.
	projectWS := workspaceClient(t, project.Status.URL)
	waitFor(t, ctx, "the default namespace to be created in the project workspace", func(ctx context.Context) (bool, error) {
		var ns corev1.Namespace
		err := projectWS.Get(ctx, client.ObjectKey{Name: "default"}, &ns)
		return err == nil, nil
	})
}

func TestScenarioDisplayNameCollisionsGetDistinctWorkspaces(t *testing.T) {
	ctx := testContext(t)
	suffix := randomSuffix(t)

	first := createTenant(t, ctx, "metal-one-"+suffix, "Heavy Metal "+suffix)
	second := createTenant(t, ctx, "metal-two-"+suffix, "Heavy Metal "+suffix)

	if first.Status.Workspace == second.Status.Workspace {
		t.Fatalf("both tenants got workspace %q", first.Status.Workspace)
	}
	if second.Status.WorkspaceCluster == first.Status.WorkspaceCluster {
		t.Errorf("tenants share a logical cluster: %q", first.Status.WorkspaceCluster)
	}
}

func TestScenarioHostileDisplayNamesStillProvision(t *testing.T) {
	ctx := testContext(t)

	for i, hostile := range []string{"🔥🔥🔥", "---", "ÜBER Größe"} {
		tenant := createTenant(t, ctx, "hostile-"+randomSuffix(t), hostile)
		if tenant.Status.Workspace == "" {
			t.Errorf("case %d (%q): no workspace", i, hostile)
		}
	}
}

// A project workspace is a child of the tenant's workspace, not of the
// platform tier.
func TestScenarioProjectNestsUnderTenant(t *testing.T) {
	ctx := testContext(t)

	tenantName := "nest-" + randomSuffix(t)
	tenant := createTenant(t, ctx, tenantName, "Nesting Corp")
	tenantWS := workspaceClient(t, tenant.Status.URL)
	project := createProject(t, ctx, tenantWS, "web", tenantName, "Web Shop")

	var ws kcptenancyv1alpha1.Workspace
	if err := tenantWS.Get(ctx, client.ObjectKey{Name: project.Status.Workspace}, &ws); err != nil {
		t.Fatalf("project workspace %q not found under the tenant: %v", project.Status.Workspace, err)
	}
	if ws.Spec.Type == nil || string(ws.Spec.Type.Name) != "project" {
		t.Errorf("project workspace has type %+v, want project", ws.Spec.Type)
	}
	if project.Status.WorkspaceCluster == tenant.Status.WorkspaceCluster {
		t.Errorf("project shares the tenant's logical cluster %q", tenant.Status.WorkspaceCluster)
	}
}

// A tenant-wide grant lands in the tenant's PROJECT workspaces and never in
// the tenant workspace itself.
func TestScenarioMembershipMaterializesInProjectsOnly(t *testing.T) {
	ctx := testContext(t)

	tenantName := "grant-" + randomSuffix(t)
	tenant := createTenant(t, ctx, tenantName, "Granting Corp")
	tenantWS := workspaceClient(t, tenant.Status.URL)
	web := createProject(t, ctx, tenantWS, "web", tenantName, "Web")
	api := createProject(t, ctx, tenantWS, "api", tenantName, "API")

	membership := createMembership(t, ctx, tenantWS, "alice-admin", tenancyv1alpha1.MembershipSpec{
		Subject: tenancyv1alpha1.Subject{Kind: tenancyv1alpha1.SubjectKindUser, Name: "alice"},
		Role:    tenancyv1alpha1.RoleAdmin,
		Tenant:  tenantName,
	})

	// It fans out across every project.
	for _, project := range []*tenancyv1alpha1.Project{web, api} {
		ws := workspaceClient(t, project.Status.URL)
		waitFor(t, ctx, "the grant to reach project "+project.Name, func(ctx context.Context) (bool, error) {
			crb, err := getBinding(ctx, ws, membership.Name)
			if err != nil {
				return false, nil
			}
			return crb.RoleRef.Name == "tenancy.contrib.kcp.io:role:admin", nil
		})
	}

	// And it is absent from the tenant workspace, which is the tier that
	// decides who may reach what.
	var crbs rbacv1.ClusterRoleBindingList
	if err := tenantWS.List(ctx, &crbs); err != nil {
		t.Fatalf("list bindings in the tenant workspace: %v", err)
	}
	for _, crb := range crbs.Items {
		if crb.Name == bindingName(membership.Name) {
			t.Errorf("the grant materialized in the TENANT workspace (%s); it must only land in projects", crb.Name)
		}
	}

	// Revoking removes it from every project again.
	if err := tenantWS.Delete(ctx, membership); err != nil {
		t.Fatalf("delete membership: %v", err)
	}
	for _, project := range []*tenancyv1alpha1.Project{web, api} {
		ws := workspaceClient(t, project.Status.URL)
		waitFor(t, ctx, "the grant to be revoked from project "+project.Name, func(ctx context.Context) (bool, error) {
			_, err := getBinding(ctx, ws, membership.Name)
			return isNotFound(err), nil
		})
	}
}

func TestScenarioProjectScopedGrantReachesOneProject(t *testing.T) {
	ctx := testContext(t)

	tenantName := "scoped-" + randomSuffix(t)
	tenant := createTenant(t, ctx, tenantName, "Scoped Corp")
	tenantWS := workspaceClient(t, tenant.Status.URL)
	web := createProject(t, ctx, tenantWS, "web", tenantName, "Web")
	api := createProject(t, ctx, tenantWS, "api", tenantName, "API")

	membership := createMembership(t, ctx, tenantWS, "bob-web", tenancyv1alpha1.MembershipSpec{
		Subject: tenancyv1alpha1.Subject{Kind: tenancyv1alpha1.SubjectKindUser, Name: "bob"},
		Role:    tenancyv1alpha1.RoleView,
		Tenant:  tenantName,
		Project: "web",
	})

	webWS := workspaceClient(t, web.Status.URL)
	waitFor(t, ctx, "the grant to reach project web", func(ctx context.Context) (bool, error) {
		_, err := getBinding(ctx, webWS, membership.Name)
		return err == nil, nil
	})

	apiWS := workspaceClient(t, api.Status.URL)
	if _, err := getBinding(ctx, apiWS, membership.Name); !isNotFound(err) {
		t.Errorf("a project-scoped grant reached project api as well: %v", err)
	}
}

func TestScenarioChangingTheRoleReplacesTheBinding(t *testing.T) {
	ctx := testContext(t)

	tenantName := "role-" + randomSuffix(t)
	tenant := createTenant(t, ctx, tenantName, "Role Corp")
	tenantWS := workspaceClient(t, tenant.Status.URL)
	project := createProject(t, ctx, tenantWS, "web", tenantName, "Web")

	membership := createMembership(t, ctx, tenantWS, "bob-view", tenancyv1alpha1.MembershipSpec{
		Subject: tenancyv1alpha1.Subject{Kind: tenancyv1alpha1.SubjectKindUser, Name: "bob"},
		Role:    tenancyv1alpha1.RoleView,
		Tenant:  tenantName,
	})

	membership.Spec.Role = tenancyv1alpha1.RoleEdit
	if err := tenantWS.Update(ctx, membership); err != nil {
		t.Fatalf("update membership role: %v", err)
	}

	ws := workspaceClient(t, project.Status.URL)
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

	tenantName := "review-" + randomSuffix(t)
	tenant := createTenant(t, ctx, tenantName, "Review Corp")
	tenantWS := workspaceClient(t, tenant.Status.URL)
	createProject(t, ctx, tenantWS, "web", tenantName, "Web Shop")
	createMembership(t, ctx, tenantWS, "alice-admin", tenancyv1alpha1.MembershipSpec{
		Subject: tenancyv1alpha1.Subject{Kind: tenancyv1alpha1.SubjectKindUser, Name: "alice"},
		Role:    tenancyv1alpha1.RoleAdmin,
		Tenant:  tenantName,
	})

	review := selfTenancyReview(t, ctx, "alice", "alice sees "+tenantName, func(r *tenancyv1alpha1.SelfTenancyReview) bool {
		for _, claim := range r.Status.Tenants {
			if claim.Name == tenantName && slices.Contains(claim.Roles, "admin") && len(claim.Projects) == 1 {
				return true
			}
		}
		return false
	})

	var found *tenancyv1alpha1.TenantClaim
	for i := range review.Status.Tenants {
		if review.Status.Tenants[i].Name == tenantName {
			found = &review.Status.Tenants[i]
		}
	}
	if found == nil {
		t.Fatalf("alice's review has no claim for %s: %+v", tenantName, review.Status.Tenants)
	}
	if found.Cluster != tenant.Status.WorkspaceCluster {
		t.Errorf("claim points at cluster %q, want the tenant's workspace %q", found.Cluster, tenant.Status.WorkspaceCluster)
	}
	if found.Endpoint == "" || found.DisplayName != "Review Corp" {
		t.Errorf("claim is incomplete: %+v", found)
	}
	if found.Projects[0].Name != "web" || !slices.Contains(found.Projects[0].Roles, "admin") {
		t.Errorf("tenant-wide admin should reach the project: %+v", found.Projects)
	}

	// A stranger gets an empty answer, not someone else's tenants.
	selfTenancyReview(t, ctx, "mallory", "mallory sees nothing for "+tenantName, func(r *tenancyv1alpha1.SelfTenancyReview) bool {
		for _, claim := range r.Status.Tenants {
			if claim.Name == tenantName {
				return false
			}
		}
		return true
	})
}

func TestScenarioGroupMembershipReachesGroupMembers(t *testing.T) {
	ctx := testContext(t)

	tenantName := "group-" + randomSuffix(t)
	tenant := createTenant(t, ctx, tenantName, "Group Corp")
	tenantWS := workspaceClient(t, tenant.Status.URL)
	createProject(t, ctx, tenantWS, "web", tenantName, "Web")
	createMembership(t, ctx, tenantWS, "team-a-edit", tenancyv1alpha1.MembershipSpec{
		Subject: tenancyv1alpha1.Subject{Kind: tenancyv1alpha1.SubjectKindGroup, Name: "team-a"},
		Role:    tenancyv1alpha1.RoleEdit,
		Tenant:  tenantName,
	})

	// alice's certificate carries O=team-a; bob's carries O=team-b.
	selfTenancyReview(t, ctx, "alice", "alice reaches "+tenantName+" through team-a", func(r *tenancyv1alpha1.SelfTenancyReview) bool {
		for _, claim := range r.Status.Tenants {
			if claim.Name == tenantName && slices.Contains(claim.Roles, "edit") {
				return true
			}
		}
		return false
	})
	selfTenancyReview(t, ctx, "bob", "bob does not reach "+tenantName, func(r *tenancyv1alpha1.SelfTenancyReview) bool {
		for _, claim := range r.Status.Tenants {
			if claim.Name == tenantName {
				return false
			}
		}
		return true
	})
}

func TestScenarioTenantDeletionRemovesTheWorkspace(t *testing.T) {
	ctx := testContext(t)

	name := "doomed-" + randomSuffix(t)
	tenant := createTenant(t, ctx, name, "Doomed Tenant")
	workspaceName := tenant.Status.Workspace

	store := storeClient(t)
	if err := store.Delete(ctx, tenant); err != nil {
		t.Fatalf("delete tenant: %v", err)
	}

	waitFor(t, ctx, "tenant object gone", func(ctx context.Context) (bool, error) {
		err := store.Get(ctx, client.ObjectKey{Name: name}, &tenancyv1alpha1.Tenant{})
		return isNotFound(err), nil
	})
	waitFor(t, ctx, "tenant workspace gone", func(ctx context.Context) (bool, error) {
		err := adminClient(t, tenantsPath).Get(ctx, client.ObjectKey{Name: workspaceName}, &kcptenancyv1alpha1.Workspace{})
		return isNotFound(err), nil
	})
}

var workspaceGVR = schema.GroupVersionResource{
	Group: "tenancy.kcp.io", Version: "v1alpha1", Resource: "workspaces",
}

func hasPrefix(s, prefix string) bool { return len(s) >= len(prefix) && s[:len(prefix)] == prefix }

// A tenant-wide grant must reach a project created AFTER it. The grant
// fans out across the projects that exist when it reconciles, so without
// the membership controller also watching Projects this silently does
// nothing — an authorization system that quietly fails to apply a grant.
func TestScenarioTenantWideGrantReachesALaterProject(t *testing.T) {
	ctx := testContext(t)

	tenantName := "late-" + randomSuffix(t)
	tenant := createTenant(t, ctx, tenantName, "Late Corp")
	tenantWS := workspaceClient(t, tenant.Status.URL)

	// The grant exists before any project does.
	early := createProject(t, ctx, tenantWS, "early", tenantName, "Early")
	membership := createMembership(t, ctx, tenantWS, "alice-admin", tenancyv1alpha1.MembershipSpec{
		Subject: tenancyv1alpha1.Subject{Kind: tenancyv1alpha1.SubjectKindUser, Name: "alice"},
		Role:    tenancyv1alpha1.RoleAdmin,
		Tenant:  tenantName,
	})
	earlyWS := workspaceClient(t, early.Status.URL)
	waitFor(t, ctx, "the grant to reach the project that already existed", func(ctx context.Context) (bool, error) {
		_, err := getBinding(ctx, earlyWS, membership.Name)
		return err == nil, nil
	})

	// ...and only then is a second project created.
	late := createProject(t, ctx, tenantWS, "late", tenantName, "Late")
	lateWS := workspaceClient(t, late.Status.URL)
	waitFor(t, ctx, "the existing grant to reach the project created afterwards", func(ctx context.Context) (bool, error) {
		_, err := getBinding(ctx, lateWS, membership.Name)
		return err == nil, nil
	})
}
