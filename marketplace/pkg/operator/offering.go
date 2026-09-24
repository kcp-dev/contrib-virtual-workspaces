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
	"reflect"
	"sort"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"

	marketplacev1alpha1 "github.com/kcp-dev/contrib-virtual-workspaces/marketplace/sdk/apis/marketplace/v1alpha1"
)

// bindRoleName is the per-offering grant: `bind` on exactly one export.
// Publishing creates it, deleting the Offering revokes it — an offering
// off the catalog is an offering nobody new can enable.
func bindRoleName(export string) string {
	return "marketplace:bind:" + export
}

// reconcileOffering validates an Offering against the APIExport it names,
// records the validated claim surface (and bumps its revision when it
// changes), and materializes the bind grant.
func (r *reconcilers) reconcileOffering(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("cluster", req.ClusterName, "offering", req.Name)

	c, err := clientFor(ctx, r.m.publish, req.ClusterName)
	if err != nil {
		return ctrl.Result{}, err
	}

	var offering marketplacev1alpha1.Offering
	if err := c.Get(ctx, req.NamespacedName, &offering); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !offering.DeletionTimestamp.IsZero() {
		if err := r.removeBindGrant(ctx, c, offering.Spec.Export); err != nil {
			return ctrl.Result{}, err
		}
		if controllerutil.RemoveFinalizer(&offering, finalizer) {
			if err := c.Update(ctx, &offering); err != nil {
				return ctrl.Result{}, client.IgnoreNotFound(err)
			}
			logger.Info("offering cleaned up, bind grant revoked")
		}
		return ctrl.Result{}, nil
	}

	if controllerutil.AddFinalizer(&offering, finalizer) {
		if err := c.Update(ctx, &offering); err != nil {
			if apierrors.IsConflict(err) {
				return requeue()
			}
			return ctrl.Result{}, err
		}
	}

	// Which Provider record does this workspace publish under? The join is
	// the workspace's canonical path — read from its LogicalCluster, never
	// from anything the workspace's owner could claim — matched against
	// the record. A workspace no record names publishes into the void.
	providerName, err := r.resolveProviderFor(ctx, c, string(req.ClusterName))
	if err != nil {
		return ctrl.Result{}, updateOfferingStatus(ctx, c, &offering, marketplacev1alpha1.OfferingStatus{
			Phase:   marketplacev1alpha1.PhaseError,
			Message: err.Error(),
		})
	}

	// The export is the source of truth; spec.claims is a request against
	// it. The intersection is what consumers will accept.
	var export apisv1alpha2.APIExport
	if err := c.Get(ctx, types.NamespacedName{Name: offering.Spec.Export}, &export); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, updateOfferingStatus(ctx, c, &offering, marketplacev1alpha1.OfferingStatus{
				Phase:    marketplacev1alpha1.PhaseError,
				Provider: providerName,
				Message:  fmt.Sprintf("APIExport %q does not exist in this workspace", offering.Spec.Export),
			})
		}
		return ctrl.Result{}, err
	}

	applied, problems := intersectClaims(offering.Spec.Claims, export.Spec.PermissionClaims)
	if len(problems) > 0 {
		return ctrl.Result{}, updateOfferingStatus(ctx, c, &offering, marketplacev1alpha1.OfferingStatus{
			Phase:    marketplacev1alpha1.PhaseError,
			Provider: providerName,
			Message:  "claims not declared by the export: " + problems[0],
		})
	}

	if err := r.ensureBindGrant(ctx, c, offering.Spec.Export); err != nil {
		return ctrl.Result{}, updateOfferingStatus(ctx, c, &offering, marketplacev1alpha1.OfferingStatus{
			Phase:    marketplacev1alpha1.PhaseError,
			Provider: providerName,
			Message:  "materialize the bind grant: " + err.Error(),
		})
	}

	status := marketplacev1alpha1.OfferingStatus{
		Phase:         marketplacev1alpha1.PhaseReady,
		Provider:      providerName,
		IdentityHash:  export.Status.IdentityHash,
		Revision:      offering.Status.Revision,
		AppliedClaims: applied,
	}
	// A changed claim surface is a new revision — and a new consent. Every
	// change requires re-accepting, widening or narrowing alike: telling
	// them apart would mean this code deciding which permissions are
	// harmless, which is the consumer's call.
	if status.Revision == 0 || !claimsEqual(applied, offering.Status.AppliedClaims) {
		status.Revision = offering.Status.Revision + 1
	}
	if err := updateOfferingStatus(ctx, c, &offering, status); err != nil {
		return ctrl.Result{}, err
	}

	logger.V(2).Info("offering ready", "provider", providerName,
		"export", offering.Spec.Export, "revision", status.Revision)
	return ctrl.Result{}, nil
}

// resolveProviderFor matches the publishing workspace's canonical path
// against the Provider records, and teaches an external Provider its
// logical cluster on first contact.
func (r *reconcilers) resolveProviderFor(ctx context.Context, publishClient client.Client, cluster string) (string, error) {
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
	var providers marketplacev1alpha1.ProviderList
	if err := catalog.List(ctx, &providers); err != nil {
		return "", fmt.Errorf("list Provider records: %w", err)
	}
	for i := range providers.Items {
		p := &providers.Items[i]
		if p.Status.WorkspacePath != path {
			continue
		}
		if p.Status.WorkspaceCluster == "" {
			p.Status.WorkspaceCluster = cluster
			p.Status.Phase = marketplacev1alpha1.PhaseReady
			p.Status.Message = ""
			if err := catalog.Status().Update(ctx, p); err != nil && !apierrors.IsConflict(err) {
				return "", fmt.Errorf("record the provider's logical cluster: %w", err)
			}
		} else if p.Status.WorkspaceCluster != cluster {
			return "", fmt.Errorf("provider %q maps %s to cluster %s, but this offering came from %s",
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

func (r *reconcilers) removeBindGrant(ctx context.Context, c client.Client, export string) error {
	if export == "" {
		return nil
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

// intersectClaims validates requested claims against what the export
// declares. Verbs and identity come from the export; matchLabels come from
// the request (a provider may narrow, never widen). Anything not declared
// is a problem, not a silent drop — a catalog that hides what it refused
// would invite retry-until-it-sticks.
func intersectClaims(requested []marketplacev1alpha1.OfferingClaim, declared []apisv1alpha2.PermissionClaim) ([]marketplacev1alpha1.OfferingClaim, []string) {
	var out []marketplacev1alpha1.OfferingClaim
	var problems []string
	for _, req := range requested {
		var match *apisv1alpha2.PermissionClaim
		for i := range declared {
			if declared[i].Group == req.Group && declared[i].Resource == req.Resource {
				match = &declared[i]
				break
			}
		}
		if match == nil {
			problems = append(problems, fmt.Sprintf("%s.%s is not claimed by the export", req.Resource, req.Group))
			continue
		}
		verbs := intersectVerbs(req.Verbs, match.Verbs)
		if len(verbs) == 0 {
			problems = append(problems, fmt.Sprintf("%s.%s: none of the requested verbs are declared", req.Resource, req.Group))
			continue
		}
		out = append(out, marketplacev1alpha1.OfferingClaim{
			Group:        req.Group,
			Resource:     req.Resource,
			Verbs:        verbs,
			IdentityHash: match.IdentityHash,
			MatchLabels:  req.MatchLabels,
		})
	}
	return out, problems
}

func intersectVerbs(requested, declared []string) []string {
	declaredSet := map[string]bool{}
	wildcard := false
	for _, v := range declared {
		declaredSet[v] = true
		if v == "*" {
			wildcard = true
		}
	}
	var out []string
	for _, v := range requested {
		if wildcard || declaredSet[v] {
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}

func claimsEqual(a, b []marketplacev1alpha1.OfferingClaim) bool {
	if len(a) != len(b) {
		return false
	}
	return reflect.DeepEqual(a, b)
}

func updateOfferingStatus(ctx context.Context, c client.Client, offering *marketplacev1alpha1.Offering, status marketplacev1alpha1.OfferingStatus) error {
	if reflect.DeepEqual(offering.Status, status) {
		return nil
	}
	offering.Status = status
	if err := c.Status().Update(ctx, offering); err != nil && !apierrors.IsConflict(err) {
		return err
	}
	return nil
}
