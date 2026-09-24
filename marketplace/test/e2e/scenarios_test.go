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
	"testing"
	"time"

	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"

	marketplacev1alpha1 "github.com/kcp-dev/contrib-virtual-workspaces/marketplace/sdk/apis/marketplace/v1alpha1"
)

func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	t.Cleanup(cancel)
	return ctx
}

// The harness runs init twice; this asserts what must be there afterwards.
func TestScenarioInstallIsIdempotent(t *testing.T) {
	ctx := testContext(t)
	c := adminClient(t, exportsPath)

	for _, name := range []string{exportCatalog, exportPublish, exportProvisioner} {
		var export apisv1alpha2.APIExport
		if err := c.Get(ctx, client.ObjectKey{Name: name}, &export); err != nil {
			t.Fatalf("APIExport %s missing after double init: %v", name, err)
		}
	}
	var provisioner apisv1alpha2.APIExport
	if err := c.Get(ctx, client.ObjectKey{Name: exportProvisioner}, &provisioner); err != nil {
		t.Fatal(err)
	}
	if len(provisioner.Spec.Resources) != 0 {
		t.Fatal("marketplace-provisioner must declare no resources; the export IS the claim")
	}
}

// The two hand-bound tiers each get exactly one binding, and neither can
// do the other's job.
func TestScenarioTiersCarryTheRightBindings(t *testing.T) {
	ctx := testContext(t)

	catalog := adminClient(t, catalogPath)
	if !hasBinding(t, ctx, catalog, exportCatalog) {
		t.Fatal("the catalog workspace does not bind marketplace-catalog")
	}
	for _, absent := range []string{exportPublish, exportProvisioner} {
		if hasBinding(t, ctx, catalog, absent) {
			t.Fatalf("the catalog workspace must NOT bind %s", absent)
		}
	}

	providers := adminClient(t, providersPath)
	if !hasBinding(t, ctx, providers, exportProvisioner) {
		t.Fatal("the providers parent does not bind marketplace-provisioner")
	}
	for _, absent := range []string{exportCatalog, exportPublish} {
		if hasBinding(t, ctx, providers, absent) {
			t.Fatalf("the providers parent must NOT bind %s", absent)
		}
	}
}

// A provisioned Provider gets a workspace whose type carries exactly the
// publish binding — and, because `provider` does not extend
// root:universal, neither tenancy.kcp.io nor topology.kcp.io: a provider
// cannot spawn workspaces or inspect topology.
func TestScenarioProvisionedProviderGetsAWorkspace(t *testing.T) {
	ctx := testContext(t)
	name := "prov-" + randomSuffix(t)

	provider := registerProvider(t, ctx, name, marketplacev1alpha1.ProviderModeProvisioned, "")
	if provider.Status.WorkspacePath != providersPath+":"+name {
		t.Fatalf("provider workspace path = %q, want %q", provider.Status.WorkspacePath, providersPath+":"+name)
	}

	ws := adminClient(t, provider.Status.WorkspacePath)
	waitFor(t, ctx, "the publish binding in the provider workspace", func(ctx context.Context) (bool, error) {
		return hasBinding(t, ctx, ws, exportPublish), nil
	})
	for _, absent := range []string{exportCatalog, exportProvisioner, "tenancy.kcp.io", "topology.kcp.io"} {
		if hasBinding(t, ctx, ws, absent) {
			t.Fatalf("a provider workspace must NOT bind %s", absent)
		}
	}
}

// Publishing validates against the real export and materializes exactly
// one grant: `bind` on that export, to the configured consumers.
func TestScenarioOfferingBecomesReadyAndGrantsBind(t *testing.T) {
	ctx := testContext(t)
	sfx := randomSuffix(t)
	name := "prov-" + sfx
	exportName := "things.e2e-" + sfx + ".marketplace.contrib.kcp.io"

	provider := registerProvider(t, ctx, name, marketplacev1alpha1.ProviderModeProvisioned, "")
	ws := adminClient(t, provider.Status.WorkspacePath)
	installThingsExport(t, ctx, ws, exportName)

	offering := publishOffering(t, ctx, ws, "things", exportName, []marketplacev1alpha1.OfferingClaim{
		{Resource: "configmaps", Verbs: []string{"get", "list", "watch"}},
	})

	if offering.Status.Provider != name {
		t.Fatalf("offering resolved to provider %q, want %q", offering.Status.Provider, name)
	}
	if offering.Status.Revision != 1 {
		t.Fatalf("first revision = %d, want 1", offering.Status.Revision)
	}
	if len(offering.Status.AppliedClaims) != 1 || offering.Status.AppliedClaims[0].Resource != "configmaps" {
		t.Fatalf("applied claims = %+v, want exactly the configmaps claim", offering.Status.AppliedClaims)
	}

	var role rbacv1.ClusterRole
	if err := ws.Get(ctx, client.ObjectKey{Name: "marketplace:bind:" + exportName}, &role); err != nil {
		t.Fatalf("the bind ClusterRole was not materialized: %v", err)
	}
	if len(role.Rules) != 1 || role.Rules[0].Verbs[0] != "bind" || role.Rules[0].ResourceNames[0] != exportName {
		t.Fatalf("bind role grants %+v, want bind on %s only", role.Rules, exportName)
	}

	// An offering that requests a claim the export never declared is
	// refused, not trimmed silently.
	bad := publishOfferingExpectError(t, ctx, ws, "things-bad", exportName, []marketplacev1alpha1.OfferingClaim{
		{Resource: "clusterrolebindings", Group: "rbac.authorization.k8s.io", Verbs: []string{"*"}},
	})
	if bad.Status.Phase != marketplacev1alpha1.PhaseError {
		t.Fatalf("undeclared claim produced phase %s, want Error", bad.Status.Phase)
	}
}

func publishOfferingExpectError(t *testing.T, ctx context.Context, c client.Client, name, exportName string, claims []marketplacev1alpha1.OfferingClaim) *marketplacev1alpha1.Offering {
	t.Helper()
	offering := &marketplacev1alpha1.Offering{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       marketplacev1alpha1.OfferingSpec{DisplayName: name, Export: exportName, Claims: claims},
	}
	if err := c.Create(ctx, offering); err != nil {
		t.Fatalf("create offering %s: %v", name, err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		_ = c.Delete(cleanupCtx, &marketplacev1alpha1.Offering{ObjectMeta: metav1.ObjectMeta{Name: name}})
	})
	waitFor(t, ctx, "offering "+name+" to error", func(ctx context.Context) (bool, error) {
		if err := c.Get(ctx, client.ObjectKey{Name: name}, offering); err != nil {
			return false, nil
		}
		return offering.Status.Phase == marketplacev1alpha1.PhaseError, nil
	})
	return offering
}

// standUpOffering is the shared spine: a provisioned provider publishing a
// things offering, and a PLAIN consumer workspace — the marketplace needs
// nothing bound there, which is the point.
func standUpOffering(t *testing.T, ctx context.Context) (providerPath string, offering *marketplacev1alpha1.Offering, consumer client.Client, consumerPath, consumerCluster string) {
	t.Helper()
	sfx := randomSuffix(t)
	providerName := "prov-" + sfx
	exportName := "things.e2e-" + sfx + ".marketplace.contrib.kcp.io"

	provider := registerProvider(t, ctx, providerName, marketplacev1alpha1.ProviderModeProvisioned, "")
	ws := adminClient(t, provider.Status.WorkspacePath)
	installThingsExport(t, ctx, ws, exportName)
	offering = publishOffering(t, ctx, ws, "things", exportName, []marketplacev1alpha1.OfferingClaim{
		{Resource: "configmaps", Verbs: []string{"get", "list", "watch"}},
	})

	consumerWS := createWorkspace(t, ctx, "consumer-"+sfx, "", "")
	return provider.Status.WorkspacePath, offering, adminClient(t, "root:"+consumerWS.Name), "root:" + consumerWS.Name, consumerWS.Spec.Cluster
}

// Enabling is a plain kcp APIBinding the consumer creates: kcp checks the
// bind grant publishing materialized, and the provider's API serves.
func TestScenarioNativeBindEnablesAnOffering(t *testing.T) {
	ctx := testContext(t)
	providerPath, offering, consumer, consumerPath, _ := standUpOffering(t, ctx)

	bindOffering(t, ctx, consumer, "things", providerPath, offering)

	var binding apisv1alpha2.APIBinding
	if err := consumer.Get(ctx, client.ObjectKey{Name: "things"}, &binding); err != nil {
		t.Fatal(err)
	}
	if len(binding.Spec.PermissionClaims) != 1 || binding.Spec.PermissionClaims[0].Resource != "configmaps" {
		t.Fatalf("binding claims = %+v, want exactly the declared configmaps claim", binding.Spec.PermissionClaims)
	}

	dyn, err := dynamic.NewForConfig(adminRestConfig(t, consumerPath))
	if err != nil {
		t.Fatalf("dynamic client: %v", err)
	}
	gvr := schema.GroupVersionResource{
		Group:    offering.Spec.Export[len("things."):],
		Version:  "v1alpha1",
		Resource: "things",
	}
	thing := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": gvr.Group + "/v1alpha1",
		"kind":       "Thing",
		"metadata":   map[string]interface{}{"name": "proof"},
		"spec":       map[string]interface{}{"message": "enabled through a plain kcp bind"},
	}}
	waitFor(t, ctx, "the enabled API to serve", func(ctx context.Context) (bool, error) {
		_, err := dyn.Resource(gvr).Create(ctx, thing, metav1.CreateOptions{})
		return err == nil || apierrors.IsAlreadyExists(err), nil
	})

	// Disable is deleting the binding; the API stops serving.
	if err := consumer.Delete(ctx, &binding); err != nil {
		t.Fatal(err)
	}
	waitFor(t, ctx, "the API to stop serving after unbind", func(ctx context.Context) (bool, error) {
		_, err := dyn.Resource(gvr).Create(ctx, thing.DeepCopy(), metav1.CreateOptions{})
		return err != nil && !apierrors.IsAlreadyExists(err), nil
	})
}

// Any claim change is a new revision in the catalog — and the consumer's
// existing binding is structurally untouchable: the claims live on THEIR
// object, which nothing in the marketplace can write.
func TestScenarioClaimChangesBumpTheRevision(t *testing.T) {
	ctx := testContext(t)
	providerPath, offering, consumer, _, _ := standUpOffering(t, ctx)
	bindOffering(t, ctx, consumer, "things", providerPath, offering)

	catalog := adminClient(t, catalogPath)
	var rec marketplacev1alpha1.Provider
	if err := catalog.Get(ctx, client.ObjectKey{Name: offering.Status.Provider}, &rec); err != nil {
		t.Fatal(err)
	}
	ws := adminClient(t, rec.Status.WorkspacePath)

	var fresh marketplacev1alpha1.Offering
	if err := ws.Get(ctx, client.ObjectKey{Name: offering.Name}, &fresh); err != nil {
		t.Fatal(err)
	}
	fresh.Spec.Claims = append(fresh.Spec.Claims, marketplacev1alpha1.OfferingClaim{
		Resource: "secrets", Verbs: []string{"get", "list", "watch"},
		MatchLabels: map[string]string{"marketplace.contrib.kcp.io/owner": offering.Status.Provider},
	})
	if err := ws.Update(ctx, &fresh); err != nil {
		t.Fatal(err)
	}

	waitFor(t, ctx, "the offering to reach revision 2", func(ctx context.Context) (bool, error) {
		if err := ws.Get(ctx, client.ObjectKey{Name: offering.Name}, &fresh); err != nil {
			return false, nil
		}
		return fresh.Status.Revision == 2, nil
	})
	found := false
	for _, claim := range fresh.Status.AppliedClaims {
		if claim.Resource == "secrets" && len(claim.MatchLabels) > 0 {
			found = true
		}
	}
	if !found {
		t.Fatalf("revision 2 applied claims lost the scoped secrets claim: %+v", fresh.Status.AppliedClaims)
	}

	// The consumer's binding still carries revision 1's claims, verbatim.
	var binding apisv1alpha2.APIBinding
	if err := consumer.Get(ctx, client.ObjectKey{Name: "things"}, &binding); err != nil {
		t.Fatal(err)
	}
	if len(binding.Spec.PermissionClaims) != 1 {
		t.Fatalf("the consumer's binding changed without the consumer: %+v", binding.Spec.PermissionClaims)
	}
}

// Deleting the Offering revokes the bind grant: an offering off the
// catalog is an offering no USER can newly enable — being admin of one's
// own workspace grants nothing on the provider's side. (The shard admin
// is exempt: system:masters bypasses authorization, here as everywhere
// in Kubernetes — which this test found out the hard way.)
func TestScenarioDeletingTheOfferingRevokesBind(t *testing.T) {
	ctx := testContext(t)
	providerPath, offering, consumer, consumerPath, _ := standUpOffering(t, ctx)

	// alice is full admin of the consumer workspace...
	grantWorkspaceAdmin(t, ctx, consumer, "alice")
	alice, err := client.New(userKcpConfig(t, "alice", consumerPath), client.Options{Scheme: testScheme})
	if err != nil {
		t.Fatal(err)
	}

	catalog := adminClient(t, catalogPath)
	var rec marketplacev1alpha1.Provider
	if err := catalog.Get(ctx, client.ObjectKey{Name: offering.Status.Provider}, &rec); err != nil {
		t.Fatal(err)
	}
	ws := adminClient(t, rec.Status.WorkspacePath)
	if err := ws.Delete(ctx, &marketplacev1alpha1.Offering{ObjectMeta: metav1.ObjectMeta{Name: offering.Name}}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, ctx, "the bind grant to be revoked", func(ctx context.Context) (bool, error) {
		var role rbacv1.ClusterRole
		err := ws.Get(ctx, client.ObjectKey{Name: "marketplace:bind:" + offering.Spec.Export}, &role)
		return apierrors.IsNotFound(err), nil
	})

	// ...and it does not help: her rights end at her workspace, and the
	// export side's grant is gone.
	binding := bindingFor("too-late", providerPath, offering)
	waitFor(t, ctx, "the unpublished export to refuse alice", func(ctx context.Context) (bool, error) {
		err := alice.Create(ctx, binding.DeepCopy())
		if err == nil {
			t.Fatal("alice bound an unpublished export; the bind grant did not gate it")
		}
		return apierrors.IsForbidden(err), nil
	})
}
