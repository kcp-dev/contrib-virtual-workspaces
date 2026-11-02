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

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"

	marketplacev1alpha1 "github.com/kcp-dev/contrib-virtual-workspaces/marketplace/sdk/apis/marketplace/v1alpha1"
)

// Any authenticated caller can browse the catalog through the virtual
// workspace, and what they read is everything enabling needs: the export,
// the workspace path to reference, and the claims at the current revision.
func TestScenarioVWListsTheCatalog(t *testing.T) {
	ctx := testContext(t)
	producerPath, producerName, export, _, _, _ := standUpOffering(t, ctx)

	alice := userVWClient(t, "alice", "")
	entryName := producerName + "." + export.Name

	var entry marketplacev1alpha1.CatalogOffering
	waitFor(t, ctx, "the catalog to list the offering", func(ctx context.Context) (bool, error) {
		if err := alice.Get(ctx, client.ObjectKey{Name: entryName}, &entry); err != nil {
			return false, nil
		}
		return entry.Ready, nil
	})
	if entry.Producer != producerName || entry.Offering != export.Name {
		t.Fatalf("catalog entry misjoined: %+v", entry)
	}
	// The offering and the export are the same object, so the entry names
	// the export twice over: as what it is, and as what to bind.
	if entry.WorkspacePath != producerPath || entry.Export != export.Name {
		t.Fatalf("catalog entry does not carry the bind coordinates: %+v", entry)
	}
	if len(entry.Claims) == 0 || entry.Claims[0].Resource != "configmaps" {
		t.Fatalf("the catalog entry does not carry the consent surface: %+v", entry.Claims)
	}

	var list marketplacev1alpha1.CatalogOfferingList
	if err := alice.List(ctx, &list); err != nil {
		t.Fatalf("list the catalog as alice: %v", err)
	}
	found := false
	for _, item := range list.Items {
		if item.Name == entryName {
			found = true
		}
	}
	if !found {
		t.Fatalf("the catalog listing does not contain %s", entryName)
	}
}

// Enabling is governed by the consumer workspace's own RBAC and kcp's
// bind check, natively: whoever a tenancy system made admin there can
// bind, whoever holds nothing cannot even enter. This is the tandem, with
// no marketplace machinery in the path at all.
func TestScenarioAUserBindsWithTheirOwnCredential(t *testing.T) {
	ctx := testContext(t)
	producerPath, _, export, consumer, consumerPath, _ := standUpOffering(t, ctx)

	aliceCfg := userKcpConfig(t, "alice", consumerPath)
	alice, err := client.New(aliceCfg, client.Options{Scheme: testScheme})
	if err != nil {
		t.Fatal(err)
	}
	bobCfg := userKcpConfig(t, "bob", consumerPath)
	bob, err := client.New(bobCfg, client.Options{Scheme: testScheme})
	if err != nil {
		t.Fatal(err)
	}

	binding := bindingFor("things", producerPath, export)

	// No grant yet: alice cannot even enter the workspace.
	if err := alice.Create(ctx, binding.DeepCopy()); err == nil {
		t.Fatal("alice bound without any RBAC in the consumer workspace")
	}

	// The grant a tenancy admin Membership would materialize.
	grantWorkspaceAdmin(t, ctx, consumer, "alice")

	waitFor(t, ctx, "alice's native bind to succeed", func(ctx context.Context) (bool, error) {
		err := alice.Create(ctx, binding.DeepCopy())
		return err == nil || apierrors.IsAlreadyExists(err), nil
	})
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		_ = consumer.Delete(cleanupCtx, &apisv1alpha2.APIBinding{ObjectMeta: metav1.ObjectMeta{Name: "things"}})
	})
	waitFor(t, ctx, "alice's binding to be bound", func(ctx context.Context) (bool, error) {
		var got apisv1alpha2.APIBinding
		if err := alice.Get(ctx, client.ObjectKey{Name: "things"}, &got); err != nil {
			return false, nil
		}
		return got.Status.Phase == apisv1alpha2.APIBindingPhaseBound, nil
	})

	// bob holds nothing there: refused, by the workspace, not by us.
	if err := bob.Create(ctx, bindingFor("bobs", producerPath, export)); err == nil {
		t.Fatal("bob bound with no RBAC in the consumer workspace")
	}

	// And alice can disable, natively.
	if err := alice.Delete(ctx, &apisv1alpha2.APIBinding{ObjectMeta: metav1.ObjectMeta{Name: "things"}}); err != nil {
		t.Fatalf("alice unbinding: %v", err)
	}
}

// A BYO provider needs no provisioning: any workspace becomes a provider
// workspace by binding marketplace-publish itself — the binding is the
// consent — and a Provider record naming its path makes its offerings
// resolvable.
func TestScenarioExternalProviderPublishesByBinding(t *testing.T) {
	ctx := testContext(t)
	sfx := randomSuffix(t)
	wsName := "byo-" + sfx
	producerName := "byo-prov-" + sfx
	exportName := "things.byo-" + sfx + ".marketplace.platform.kcp.io"

	ws := createWorkspace(t, ctx, wsName, "", "")
	byo := adminClient(t, "root:"+wsName)

	// The consent: the workspace's own admin binds marketplace-publish,
	// accepting its claims.
	binding := &apisv1alpha2.APIBinding{
		ObjectMeta: metav1.ObjectMeta{Name: exportPublish},
		Spec: apisv1alpha2.APIBindingSpec{
			Reference: apisv1alpha2.BindingReference{
				Export: &apisv1alpha2.ExportBindingReference{Path: exportsPath, Name: exportPublish},
			},
			PermissionClaims: acceptedPublishClaims(),
		},
	}
	waitFor(t, ctx, "binding marketplace-publish by hand", func(ctx context.Context) (bool, error) {
		err := byo.Create(ctx, binding.DeepCopy())
		return err == nil || apierrors.IsAlreadyExists(err), nil
	})

	registerProducer(t, ctx, producerName, marketplacev1alpha1.ProducerModeExternal, "root:"+wsName)
	installThingsExport(t, ctx, byo, exportName)
	export := publishExport(t, ctx, byo, exportName, "things")

	// Publishing taught the Provider record its cluster.
	catalog := adminClient(t, catalogPath)
	var rec marketplacev1alpha1.Producer
	if err := catalog.Get(ctx, client.ObjectKey{Name: producerName}, &rec); err != nil {
		t.Fatal(err)
	}
	if rec.Status.WorkspaceCluster != ws.Spec.Cluster {
		t.Fatalf("provider learned cluster %q, want %q", rec.Status.WorkspaceCluster, ws.Spec.Cluster)
	}

	// And a consumer can enable it like any platform offering: a plain
	// workspace, a plain bind.
	consumerWS := createWorkspace(t, ctx, "consumer-"+sfx, "", "")
	consumer := adminClient(t, "root:"+consumerWS.Name)
	bindOffering(t, ctx, consumer, "things", "root:"+wsName, export)
}

// acceptedPublishClaims mirrors, on the consumer side, every claim the
// publish export declares — what kcp's default-binding controller does for
// typed workspaces, spelled out here because BYO binds by hand.
func acceptedPublishClaims() []apisv1alpha2.AcceptablePermissionClaim {
	accept := func(group, resource string, verbs ...string) apisv1alpha2.AcceptablePermissionClaim {
		return apisv1alpha2.AcceptablePermissionClaim{
			ScopedPermissionClaim: apisv1alpha2.ScopedPermissionClaim{
				PermissionClaim: apisv1alpha2.PermissionClaim{
					GroupResource: apisv1alpha2.GroupResource{Group: group, Resource: resource},
					Verbs:         verbs,
				},
				Selector: apisv1alpha2.PermissionClaimSelector{MatchAll: true},
			},
			State: apisv1alpha2.ClaimAccepted,
		}
	}
	return []apisv1alpha2.AcceptablePermissionClaim{
		accept("apis.kcp.io", "apiexports", "get", "list", "watch"),
		accept("apis.kcp.io", "apiexportendpointslices", "get", "list", "watch"),
		accept("apis.kcp.io", "apiresourceschemas", "get", "list", "watch"),
		accept("core.kcp.io", "logicalclusters", "get", "list", "watch"),
		accept("rbac.authorization.k8s.io", "clusterroles", "get", "list", "watch", "create", "update", "delete"),
		accept("rbac.authorization.k8s.io", "clusterrolebindings", "get", "list", "watch", "create", "update", "delete"),
	}
}
