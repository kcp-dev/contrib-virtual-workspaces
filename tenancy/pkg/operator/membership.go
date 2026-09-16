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

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"

	tenancyv1alpha1 "github.com/kcp-dev/contrib-virtual-workspaces/tenancy/sdk/apis/tenancy/v1alpha1"
)

func (r *reconcilers) reconcileMembership(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("cluster", req.ClusterName, "membership", req.Name)

	c, err := r.clusterClient(ctx, req.ClusterName)
	if err != nil {
		return ctrl.Result{}, err
	}

	var membership tenancyv1alpha1.Membership
	if err := c.Get(ctx, req.NamespacedName, &membership); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	targetURL, pending, err := r.resolveTargetWorkspace(ctx, c, &membership)

	if !membership.DeletionTimestamp.IsZero() {
		// Best effort: when the target workspace is gone (tenant deleted,
		// workspace deleted), the binding went with it.
		if err == nil && !pending && targetURL != "" {
			target, dErr := r.directClient(targetURL)
			if dErr != nil {
				return ctrl.Result{}, dErr
			}
			crb := rbacv1.ClusterRoleBinding{}
			crb.Name = MembershipBindingName(membership.Name)
			if dErr := target.Delete(ctx, &crb); dErr != nil && !apierrors.IsNotFound(dErr) {
				return ctrl.Result{}, fmt.Errorf("delete binding: %w", dErr)
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
		return requeue2(updateMembershipStatus(ctx, c, &membership, tenancyv1alpha1.MembershipStatus{
			Phase:   tenancyv1alpha1.PhaseError,
			Message: err.Error(),
		}))
	}
	if pending {
		return requeue2(updateMembershipStatus(ctx, c, &membership, tenancyv1alpha1.MembershipStatus{
			Phase:   tenancyv1alpha1.PhasePending,
			Message: "waiting for the target workspace",
		}))
	}

	target, err := r.directClient(targetURL)
	if err != nil {
		return ctrl.Result{}, err
	}
	if err := r.applyRBAC(ctx, target, &membership); err != nil {
		if statusErr := updateMembershipStatus(ctx, c, &membership, tenancyv1alpha1.MembershipStatus{
			Phase:   tenancyv1alpha1.PhaseError,
			Message: err.Error(),
		}); statusErr != nil {
			logger.Error(statusErr, "writing error status")
		}
		return ctrl.Result{}, err
	}

	logger.V(2).Info("membership applied", "role", membership.Spec.Role, "subject", membership.Spec.Subject.Name)
	return ctrl.Result{}, updateMembershipStatus(ctx, c, &membership, tenancyv1alpha1.MembershipStatus{
		Phase: tenancyv1alpha1.PhaseReady,
	})
}

// resolveTargetWorkspace finds the workspace URL a membership's RBAC lands
// in: the tenant's workspace, or the project's when the grant is
// project-scoped. pending is true while the workspace is still coming up.
func (r *reconcilers) resolveTargetWorkspace(ctx context.Context, c client.Client, membership *tenancyv1alpha1.Membership) (targetURL string, pending bool, err error) {
	var tenant tenancyv1alpha1.Tenant
	if err := c.Get(ctx, types.NamespacedName{Name: membership.Spec.Tenant}, &tenant); err != nil {
		if apierrors.IsNotFound(err) {
			return "", false, fmt.Errorf("tenant %q not found in this workspace", membership.Spec.Tenant)
		}
		return "", false, err
	}

	if membership.Spec.Project == "" {
		if tenant.Status.Phase != tenancyv1alpha1.PhaseReady || tenant.Status.URL == "" {
			return "", true, nil
		}
		return tenant.Status.URL, false, nil
	}

	var project tenancyv1alpha1.Project
	if err := c.Get(ctx, types.NamespacedName{Name: membership.Spec.Project}, &project); err != nil {
		if apierrors.IsNotFound(err) {
			return "", false, fmt.Errorf("project %q not found in this workspace", membership.Spec.Project)
		}
		return "", false, err
	}
	if project.Spec.Tenant != membership.Spec.Tenant {
		return "", false, fmt.Errorf("project %q belongs to tenant %q, not %q",
			membership.Spec.Project, project.Spec.Tenant, membership.Spec.Tenant)
	}
	if project.Status.Phase != tenancyv1alpha1.PhaseReady || project.Status.URL == "" {
		return "", true, nil
	}
	return project.Status.URL, false, nil
}

// applyRBAC materializes the role's ClusterRole and the membership's
// binding in the target workspace.
func (r *reconcilers) applyRBAC(ctx context.Context, target client.Client, membership *tenancyv1alpha1.Membership) error {
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
		// The operator owns these roles; drift (or an older version's
		// rules) is repaired, not respected.
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

	// roleRef is immutable on ClusterRoleBindings, so a changed role means
	// replace, not update. Subjects alone could be updated in place, but one
	// path is simpler and replacement is idempotent either way.
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
