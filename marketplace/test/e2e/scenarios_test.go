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

	"github.com/kcp-dev/contrib-virtual-workspaces/marketplace/pkg/offering"
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

	providers := adminClient(t, producersPath)
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

	provider := registerProducer(t, ctx, name, marketplacev1alpha1.ProducerModeProvisioned, "")
	if provider.Status.WorkspacePath != producersPath+":"+name {
		t.Fatalf("provider workspace path = %q, want %q", provider.Status.WorkspacePath, producersPath+":"+name)
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

// Publishing is annotating the export, and it materializes exactly one
// grant: `bind` on that export, to the configured consumers.
func TestScenarioAnnotatingAnExportPublishesAndGrantsBind(t *testing.T) {
	ctx := testContext(t)
	sfx := randomSuffix(t)
	name := "prov-" + sfx
	exportName := "things.e2e-" + sfx + ".marketplace.platform.kcp.io"

	provider := registerProducer(t, ctx, name, marketplacev1alpha1.ProducerModeProvisioned, "")
	ws := adminClient(t, provider.Status.WorkspacePath)
	installThingsExport(t, ctx, ws, exportName)

	// Before the annotation there is no grant: an export is not on the
	// catalog merely by existing in a registered provider's workspace.
	var role rbacv1.ClusterRole
	if err := ws.Get(ctx, client.ObjectKey{Name: "marketplace:bind:" + exportName}, &role); !apierrors.IsNotFound(err) {
		t.Fatalf("an unannotated export already has a bind grant (%v)", err)
	}

	export := publishExport(t, ctx, ws, exportName, "things")

	if err := ws.Get(ctx, client.ObjectKey{Name: "marketplace:bind:" + exportName}, &role); err != nil {
		t.Fatalf("the bind ClusterRole was not materialized: %v", err)
	}
	if len(role.Rules) != 1 || role.Rules[0].Verbs[0] != "bind" || role.Rules[0].ResourceNames[0] != exportName {
		t.Fatalf("bind role grants %+v, want bind on %s only", role.Rules, exportName)
	}

	// The claim surface is the export's own: there is no second place to
	// declare it, so there is nothing that could disagree with it.
	claims := offering.Claims(export.Spec.PermissionClaims)
	if len(claims) != 1 || claims[0].Resource != "configmaps" {
		t.Fatalf("claims = %+v, want exactly the export's configmaps claim", claims)
	}

	// Un-publishing revokes it, without deleting anything.
	unpublishExport(t, ctx, ws, exportName)
	waitFor(t, ctx, "the bind grant to be revoked on un-publish", func(ctx context.Context) (bool, error) {
		err := ws.Get(ctx, client.ObjectKey{Name: "marketplace:bind:" + exportName}, &role)
		return apierrors.IsNotFound(err), nil
	})
}

// standUpOffering is the shared spine: a provisioned provider publishing a
// things offering, and a PLAIN consumer workspace — the marketplace needs
// nothing bound there, which is the point.
func standUpOffering(t *testing.T, ctx context.Context) (producerPath, producerName string, export *apisv1alpha2.APIExport, consumer client.Client, consumerPath, consumerCluster string) {
	t.Helper()
	sfx := randomSuffix(t)
	producerName = "prov-" + sfx
	exportName := "things.e2e-" + sfx + ".marketplace.platform.kcp.io"

	provider := registerProducer(t, ctx, producerName, marketplacev1alpha1.ProducerModeProvisioned, "")
	ws := adminClient(t, provider.Status.WorkspacePath)
	installThingsExport(t, ctx, ws, exportName)
	export = publishExport(t, ctx, ws, exportName, "things")

	consumerWS := createWorkspace(t, ctx, "consumer-"+sfx, "", "")
	return provider.Status.WorkspacePath, producerName, export,
		adminClient(t, "root:"+consumerWS.Name), "root:" + consumerWS.Name, consumerWS.Spec.Cluster
}

// Enabling is a plain kcp APIBinding the consumer creates: kcp checks the
// bind grant publishing materialized, and the provider's API serves.
func TestScenarioNativeBindEnablesAnOffering(t *testing.T) {
	ctx := testContext(t)
	producerPath, _, export, consumer, consumerPath, _ := standUpOffering(t, ctx)

	bindOffering(t, ctx, consumer, "things", producerPath, export)

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
		Group:    export.Name[len("things."):],
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

// Widening the export's claims changes the revision the catalog
// advertises — and the consumer's existing binding is structurally
// untouchable: the claims live on THEIR object, which nothing in the
// marketplace can write.
func TestScenarioClaimChangesChangeTheRevision(t *testing.T) {
	ctx := testContext(t)
	producerPath, producerName, export, consumer, _, _ := standUpOffering(t, ctx)
	bindOffering(t, ctx, consumer, "things", producerPath, export)

	alice := userVWClient(t, "alice", "")
	entryName := producerName + "." + export.Name

	var before marketplacev1alpha1.CatalogOffering
	waitFor(t, ctx, "the catalog to list the offering", func(ctx context.Context) (bool, error) {
		if err := alice.Get(ctx, client.ObjectKey{Name: entryName}, &before); err != nil {
			return false, nil
		}
		return before.Ready && before.Revision != "", nil
	})

	// The provider widens the consent surface by editing the export — the
	// only place it is declared.
	ws := adminClient(t, producerPath)
	waitFor(t, ctx, "widen the export's claims", func(ctx context.Context) (bool, error) {
		var fresh apisv1alpha2.APIExport
		if err := ws.Get(ctx, client.ObjectKey{Name: export.Name}, &fresh); err != nil {
			return false, nil
		}
		fresh.Spec.PermissionClaims = append(fresh.Spec.PermissionClaims, apisv1alpha2.PermissionClaim{
			GroupResource: apisv1alpha2.GroupResource{Group: "", Resource: "secrets"},
			Verbs:         []string{"get", "list", "watch"},
			DefaultSelector: &apisv1alpha2.PermissionClaimSelector{
				LabelSelector: metav1.LabelSelector{
					MatchLabels: map[string]string{"marketplace.platform.kcp.io/owner": producerName},
				},
			},
		})
		return ws.Update(ctx, &fresh) == nil, nil
	})

	var after marketplacev1alpha1.CatalogOffering
	waitFor(t, ctx, "the catalog to advertise the wider surface", func(ctx context.Context) (bool, error) {
		if err := alice.Get(ctx, client.ObjectKey{Name: entryName}, &after); err != nil {
			return false, nil
		}
		return len(after.Claims) == 2, nil
	})
	if after.Revision == before.Revision {
		t.Fatalf("the claim surface changed but the revision did not (%q)", after.Revision)
	}
	scoped := false
	for _, claim := range after.Claims {
		if claim.Resource == "secrets" && len(claim.MatchLabels) > 0 {
			scoped = true
		}
	}
	if !scoped {
		t.Fatalf("the catalog lost the provider's suggested scope: %+v", after.Claims)
	}

	// The consumer's binding still carries what they accepted, verbatim.
	var binding apisv1alpha2.APIBinding
	if err := consumer.Get(ctx, client.ObjectKey{Name: "things"}, &binding); err != nil {
		t.Fatal(err)
	}
	if len(binding.Spec.PermissionClaims) != 1 {
		t.Fatalf("the consumer's binding changed without the consumer: %+v", binding.Spec.PermissionClaims)
	}
}

// Un-publishing revokes the bind grant: an offering off the catalog is an
// offering no USER can newly enable — being admin of one's own workspace
// grants nothing on the provider's side. (The shard admin is exempt:
// system:masters bypasses authorization, here as everywhere in Kubernetes
// — which this test found out the hard way.)
func TestScenarioUnpublishingRevokesBind(t *testing.T) {
	ctx := testContext(t)
	producerPath, _, export, consumer, consumerPath, _ := standUpOffering(t, ctx)

	// alice is full admin of the consumer workspace...
	grantWorkspaceAdmin(t, ctx, consumer, "alice")
	alice, err := client.New(userKcpConfig(t, "alice", consumerPath), client.Options{Scheme: testScheme})
	if err != nil {
		t.Fatal(err)
	}

	// The provider takes it off the catalog by removing one annotation.
	// The export itself, and everyone already bound to it, stay exactly as
	// they were.
	ws := adminClient(t, producerPath)
	unpublishExport(t, ctx, ws, export.Name)
	waitFor(t, ctx, "the bind grant to be revoked", func(ctx context.Context) (bool, error) {
		var role rbacv1.ClusterRole
		err := ws.Get(ctx, client.ObjectKey{Name: "marketplace:bind:" + export.Name}, &role)
		return apierrors.IsNotFound(err), nil
	})

	// ...and it does not help: her rights end at her workspace, and the
	// export side's grant is gone.
	binding := bindingFor("too-late", producerPath, export)
	waitFor(t, ctx, "the unpublished export to refuse alice", func(ctx context.Context) (bool, error) {
		err := alice.Create(ctx, binding.DeepCopy())
		if err == nil {
			t.Fatal("alice bound an unpublished export; the bind grant did not gate it")
		}
		return apierrors.IsForbidden(err), nil
	})
}
