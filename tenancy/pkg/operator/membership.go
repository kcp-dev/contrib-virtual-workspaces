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
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"

	tenancyv1alpha1 "github.com/kcp-dev/contrib-virtual-workspaces/tenancy/sdk/apis/tenancy/v1alpha1"
)

// A Membership lives in its tenant's workspace and materializes in that
// tenant's PROJECT workspaces — never in the tenant workspace itself.
//
// That asymmetry is deliberate. The tenant workspace holds the Memberships
// and Projects that decide who may reach what; a tenant able to write there
// could grant themselves anything, behind the virtual workspace rather than
// through it. No binding in that logical cluster names any tenant identity,
// so the tier is unreachable by construction rather than by a check.
//
// A tenant-wide grant therefore fans out across every project of the
// tenant, and a project-scoped one lands in exactly one.
func (r *reconcilers) reconcileMembership(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("cluster", req.ClusterName, "membership", req.Name)

	c, err := clientFor(ctx, r.m.tenancy, req.ClusterName)
	if err != nil {
		return ctrl.Result{}, err
	}

	var membership tenancyv1alpha1.Membership
	if err := c.Get(ctx, req.NamespacedName, &membership); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	targets, pending, err := r.targetClusters(ctx, c, &membership)

	if !membership.DeletionTimestamp.IsZero() {
		// Best effort: a project workspace that is gone took its RBAC with
		// it, so only reachable targets are cleaned.
		if err == nil {
			for _, target := range targets {
				acc, aErr := clientFor(ctx, r.m.access, target)
				if aErr != nil {
					return ctrl.Result{}, aErr
				}
				crb := rbacv1.ClusterRoleBinding{}
				crb.Name = MembershipBindingName(membership.Name)
				if dErr := acc.Delete(ctx, &crb); dErr != nil && !apierrors.IsNotFound(dErr) {
					return ctrl.Result{}, fmt.Errorf("delete binding in %s: %w", target, dErr)
				}
			}
		}
		if controllerutil.RemoveFinalizer(&membership, finalizer) {
			if err := c.Update(ctx, &membership); err != nil {
				return ctrl.Result{}, client.IgnoreNotFound(err)
			}
			logger.Info("membership revoked")
		}
		return ctrl.Result{}, nil
	}

	if controllerutil.AddFinalizer(&membership, finalizer) {
		if err := c.Update(ctx, &membership); err != nil {
			if apierrors.IsConflict(err) {
				return requeue()
			}
			return ctrl.Result{}, err
		}
	}

	if err != nil {
		return requeueAfterStatus(updateMembershipStatus(ctx, c, &membership, tenancyv1alpha1.MembershipStatus{
			Phase:   tenancyv1alpha1.PhaseError,
			Message: err.Error(),
		}))
	}
	if pending {
		return requeueAfterStatus(updateMembershipStatus(ctx, c, &membership, tenancyv1alpha1.MembershipStatus{
			Phase:   tenancyv1alpha1.PhasePending,
			Message: "waiting for a project workspace to grant in",
		}))
	}

	for _, target := range targets {
		acc, err := clientFor(ctx, r.m.access, target)
		if err != nil {
			return ctrl.Result{}, err
		}
		if err := applyRBAC(ctx, acc, &membership); err != nil {
			if statusErr := updateMembershipStatus(ctx, c, &membership, tenancyv1alpha1.MembershipStatus{
				Phase:   tenancyv1alpha1.PhaseError,
				Message: err.Error(),
			}); statusErr != nil {
				logger.Error(statusErr, "writing error status")
			}
			return ctrl.Result{}, err
		}
	}

	logger.V(2).Info("membership applied",
		"role", membership.Spec.Role, "subject", membership.Spec.Subject.Name, "projects", len(targets))
	return ctrl.Result{}, updateMembershipStatus(ctx, c, &membership, tenancyv1alpha1.MembershipStatus{
		Phase: tenancyv1alpha1.PhaseReady,
	})
}

// targetClusters resolves which project workspaces a membership lands in.
// pending is true while a named project has no workspace yet, or while a
// tenant-wide grant has no ready project to land in at all.
func (r *reconcilers) targetClusters(ctx context.Context, c client.Client, membership *tenancyv1alpha1.Membership) (targets []multicluster.ClusterName, pending bool, err error) {
	if name := membership.Spec.Project; name != "" {
		var project tenancyv1alpha1.Project
		if err := c.Get(ctx, types.NamespacedName{Name: name}, &project); err != nil {
			if apierrors.IsNotFound(err) {
				return nil, false, fmt.Errorf("project %q not found in this tenant", name)
			}
			return nil, false, err
		}
		if project.Status.Phase != tenancyv1alpha1.PhaseReady || project.Status.WorkspaceCluster == "" {
			return nil, true, nil
		}
		return []multicluster.ClusterName{multicluster.ClusterName(project.Status.WorkspaceCluster)}, false, nil
	}

	// Tenant-wide: every project of this tenant, which is every project in
	// this workspace.
	var projects tenancyv1alpha1.ProjectList
	if err := c.List(ctx, &projects); err != nil {
		return nil, false, fmt.Errorf("list projects: %w", err)
	}
	var notReady bool
	for _, p := range projects.Items {
		if p.Status.Phase != tenancyv1alpha1.PhaseReady || p.Status.WorkspaceCluster == "" {
			notReady = true
			continue
		}
		targets = append(targets, multicluster.ClusterName(p.Status.WorkspaceCluster))
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i] < targets[j] })

	// A tenant with no ready project yet is pending, not an error: the
	// grant becomes real as soon as there is somewhere to put it.
	if len(targets) == 0 && (notReady || len(projects.Items) == 0) {
		return nil, true, nil
	}
	return targets, false, nil
}

// applyRBAC materializes the role's ClusterRole and the membership's
// binding in one project workspace, through the access export.
func applyRBAC(ctx context.Context, target client.Client, membership *tenancyv1alpha1.Membership) error {
	desiredRole, err := DesiredClusterRole(membership.Spec.Role)
	if err != nil {
		return err
	}

	var existingRole rbacv1.ClusterRole
	switch err := target.Get(ctx, types.NamespacedName{Name: desiredRole.Name}, &existingRole); {
	case apierrors.IsNotFound(err):
		if err := target.Create(ctx, desiredRole); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("create role %q: %w", desiredRole.Name, err)
		}
	case err != nil:
		return fmt.Errorf("get role %q: %w", desiredRole.Name, err)
	case !reflect.DeepEqual(existingRole.Rules, desiredRole.Rules):
		// The operator owns these roles; drift is repaired, not respected.
		existingRole.Rules = desiredRole.Rules
		if err := target.Update(ctx, &existingRole); err != nil {
			return fmt.Errorf("update role %q: %w", desiredRole.Name, err)
		}
	}

	desiredBinding, err := DesiredBinding(membership)
	if err != nil {
		return err
	}

	var existingBinding rbacv1.ClusterRoleBinding
	switch err := target.Get(ctx, types.NamespacedName{Name: desiredBinding.Name}, &existingBinding); {
	case apierrors.IsNotFound(err):
		if err := target.Create(ctx, desiredBinding); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("create binding %q: %w", desiredBinding.Name, err)
		}
		return nil
	case err != nil:
		return fmt.Errorf("get binding %q: %w", desiredBinding.Name, err)
	}

	if reflect.DeepEqual(existingBinding.RoleRef, desiredBinding.RoleRef) &&
		reflect.DeepEqual(existingBinding.Subjects, desiredBinding.Subjects) {
		return nil
	}

	// roleRef is immutable, so a changed role means replace, not update.
	if err := target.Delete(ctx, &existingBinding); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("replace binding %q (delete): %w", desiredBinding.Name, err)
	}
	if err := target.Create(ctx, desiredBinding); err != nil {
		return fmt.Errorf("replace binding %q (create): %w", desiredBinding.Name, err)
	}
	return nil
}

func updateMembershipStatus(ctx context.Context, c client.Client, membership *tenancyv1alpha1.Membership, status tenancyv1alpha1.MembershipStatus) error {
	if membership.Status == status {
		return nil
	}
	membership.Status = status
	if err := c.Status().Update(ctx, membership); err != nil && !apierrors.IsConflict(err) {
		return err
	}
	return nil
}
