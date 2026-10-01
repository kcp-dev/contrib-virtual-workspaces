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

package operator

import (
	"context"
	"fmt"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"

	"github.com/kcp-dev/contrib-virtual-workspaces/marketplace/pkg/offering"
	marketplacev1alpha1 "github.com/kcp-dev/contrib-virtual-workspaces/marketplace/sdk/apis/marketplace/v1alpha1"
)

// bindRoleName is the per-offering grant: `bind` on exactly one export.
// Publishing creates it, un-publishing revokes it — an offering off the
// catalog is an offering nobody new can enable.
func bindRoleName(export string) string {
	return "marketplace:bind:" + export
}

// reconcileAPIExport is the whole publish flow: an APIExport annotated for
// the catalog gets the `bind` grant that makes it enableable, and one that
// is not — never annotated, un-annotated, or deleted — does not.
//
// Nothing is written back to the export. It belongs to the producer, and
// the operator only reads it: there is no status to maintain (the catalog
// derives what it shows) and no finalizer to add, which matters because a
// finalizer of ours on someone else's object would let a wedged marketplace
// operator block a producer from deleting their own export.
func (r *reconcilers) reconcileAPIExport(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("cluster", req.ClusterName, "export", req.Name)

	c, err := clientFor(ctx, r.m.publish, req.ClusterName)
	if err != nil {
		return ctrl.Result{}, err
	}

	var export apisv1alpha2.APIExport
	switch err := c.Get(ctx, req.NamespacedName, &export); {
	case apierrors.IsNotFound(err):
		// Deleted. The grant names the export, so it can be revoked
		// without having seen what it pointed at.
		return ctrl.Result{}, r.removeBindGrant(ctx, c, req.Name)
	case err != nil:
		return ctrl.Result{}, err
	}

	if !offering.Published(&export) || !export.DeletionTimestamp.IsZero() {
		// Either never published, or the annotation is gone, or it is on
		// its way out. All three mean the same thing: not on the catalog.
		if err := r.removeBindGrant(ctx, c, export.Name); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	// Which Provider record does this workspace publish under? The join is
	// the workspace's canonical path — read from its LogicalCluster, never
	// from anything the workspace's owner could claim — matched against
	// the record. A workspace no record names publishes into the void, and
	// gets no grant: an unregistered workspace must not be able to hand
	// `bind` to the world by annotating its own export.
	producerName, err := r.resolveProducerFor(ctx, c, string(req.ClusterName))
	if err != nil {
		logger.V(2).Info("published export has no Provider record yet; not granting bind", "reason", err.Error())
		return ctrl.Result{RequeueAfter: requeueWhileUnregistered}, nil
	}

	if err := r.ensureBindGrant(ctx, c, export.Name); err != nil {
		return ctrl.Result{}, fmt.Errorf("materialize the bind grant: %w", err)
	}

	card := offering.From(&export)
	logger.V(2).Info("offering published", "producer", producerName,
		"revision", card.Revision, "claims", len(card.Claims))
	return ctrl.Result{}, nil
}

// resolveProducerFor matches the publishing workspace's canonical path
// against the Provider records, and teaches an external Provider its
// logical cluster on first contact.
func (r *reconcilers) resolveProducerFor(ctx context.Context, publishClient client.Client, cluster string) (string, error) {
	var lc corev1alpha1.LogicalCluster
	if err := publishClient.Get(ctx, types.NamespacedName{Name: "cluster"}, &lc); err != nil {
		return "", fmt.Errorf("read this workspace's LogicalCluster: %w", err)
	}
	path := lc.Annotations["kcp.io/path"]
	if path == "" {
		return "", fmt.Errorf("this workspace's LogicalCluster carries no kcp.io/path annotation")
	}

	catalog, err := clientFor(ctx, r.m.catalog, r.catalogCluster)
	if err != nil {
		return "", err
	}
	var producers marketplacev1alpha1.ProducerList
	if err := catalog.List(ctx, &producers); err != nil {
		return "", fmt.Errorf("list Provider records: %w", err)
	}
	for i := range producers.Items {
		p := &producers.Items[i]
		if p.Status.WorkspacePath != path {
			continue
		}
		if p.Status.WorkspaceCluster == "" {
			p.Status.WorkspaceCluster = cluster
			p.Status.Phase = marketplacev1alpha1.PhaseReady
			p.Status.Message = ""
			if err := catalog.Status().Update(ctx, p); err != nil && !apierrors.IsConflict(err) {
				return "", fmt.Errorf("record the producer's logical cluster: %w", err)
			}
		} else if p.Status.WorkspaceCluster != cluster {
			return "", fmt.Errorf("producer %q maps %s to cluster %s, but this export is in %s",
				p.Name, path, p.Status.WorkspaceCluster, cluster)
		}
		return p.Name, nil
	}
	return "", fmt.Errorf("no Provider record names this workspace (%s); register one in the catalog", path)
}

// ensureBindGrant materializes `bind` on one export, to the configured
// consumers (default: any authenticated user). This grant IS what makes an
// offering enableable — a consumer creates the APIBinding themselves, and
// kcp checks this verb in this workspace on the way.
func (r *reconcilers) ensureBindGrant(ctx context.Context, c client.Client, export string) error {
	role := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{Name: bindRoleName(export)},
		Rules: []rbacv1.PolicyRule{{
			APIGroups:     []string{"apis.kcp.io"},
			Resources:     []string{"apiexports"},
			ResourceNames: []string{export},
			Verbs:         []string{"bind"},
		}},
	}
	binding := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: bindRoleName(export) + ":consumers"},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: role.Name},
	}
	for _, u := range r.bindUsers {
		binding.Subjects = append(binding.Subjects, rbacv1.Subject{
			APIGroup: rbacv1.GroupName, Kind: "User", Name: u,
		})
	}
	for _, g := range r.bindGroups {
		binding.Subjects = append(binding.Subjects, rbacv1.Subject{
			APIGroup: rbacv1.GroupName, Kind: "Group", Name: g,
		})
	}
	for _, obj := range []client.Object{role, binding} {
		if err := c.Create(ctx, obj); err != nil {
			if !apierrors.IsAlreadyExists(err) {
				return err
			}
		}
	}
	return nil
}

// removeBindGrant revokes `bind` on one export.
//
// Every APIExport in every producer workspace reaches this reconciler, so
// the common case by far is an export that never had a grant. It checks
// before deleting to keep that case to one read.
func (r *reconcilers) removeBindGrant(ctx context.Context, c client.Client, export string) error {
	if export == "" {
		return nil
	}
	var existing rbacv1.ClusterRole
	if err := c.Get(ctx, types.NamespacedName{Name: bindRoleName(export)}, &existing); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	for _, obj := range []client.Object{
		&rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: bindRoleName(export) + ":consumers"}},
		&rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: bindRoleName(export)}},
	} {
		if err := c.Delete(ctx, obj); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	return nil
}
