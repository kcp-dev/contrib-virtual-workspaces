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
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"

	"github.com/kcp-dev/contrib-virtual-workspaces/tenancy/pkg/bootstrap"
	"github.com/kcp-dev/contrib-virtual-workspaces/tenancy/pkg/naming"
	tenancyv1alpha1 "github.com/kcp-dev/contrib-virtual-workspaces/tenancy/sdk/apis/tenancy/v1alpha1"
)

func (r *reconcilers) reconcileTenant(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("cluster", req.ClusterName, "tenant", req.Name)

	// The Tenant object lives in the platform workspace, served by the
	// platform export.
	c, err := clientFor(ctx, r.m.platform, req.ClusterName)
	if err != nil {
		return ctrl.Result{}, err
	}
	// Its workspace is created in the provisioning parent — a different
	// tier from the store this record was read from — through the
	// provisioner export, the only one that claims `create workspaces`.
	prov, err := clientFor(ctx, r.m.provisioner, r.tenantsCluster)
	if err != nil {
		return ctrl.Result{}, err
	}

	var tenant tenancyv1alpha1.Tenant
	if err := c.Get(ctx, req.NamespacedName, &tenant); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !tenant.DeletionTimestamp.IsZero() {
		done, err := deleteWorkspace(ctx, prov, tenant.Status.Workspace, string(tenant.UID))
		if err != nil {
			return ctrl.Result{}, err
		}
		if !done {
			return requeue()
		}
		if controllerutil.RemoveFinalizer(&tenant, finalizer) {
			if err := c.Update(ctx, &tenant); err != nil {
				return ctrl.Result{}, client.IgnoreNotFound(err)
			}
			logger.Info("tenant cleaned up")
		}
		return ctrl.Result{}, nil
	}

	if controllerutil.AddFinalizer(&tenant, finalizer) {
		if err := c.Update(ctx, &tenant); err != nil {
			if apierrors.IsConflict(err) {
				return requeue()
			}
			return ctrl.Result{}, err
		}
	}

	ws, err := ensureWorkspace(ctx, prov,
		tenant.Spec.DisplayName, string(tenant.UID),
		r.strategy.Propose(tenant.Spec.DisplayName, string(tenant.UID)),
		bootstrap.WorkspaceTypeTenant, r.exportsPath)
	if err != nil {
		if statusErr := updateTenantStatus(ctx, c, &tenant, tenancyv1alpha1.TenantStatus{
			Phase:   tenancyv1alpha1.PhaseError,
			Message: err.Error(),
		}); statusErr != nil {
			logger.Error(statusErr, "writing error status")
		}
		return ctrl.Result{}, err
	}

	status := tenancyv1alpha1.TenantStatus{
		Phase:            tenancyv1alpha1.PhasePending,
		Message:          "waiting for the workspace to become ready",
		Workspace:        ws.Name,
		WorkspaceCluster: ws.Cluster,
		URL:              ws.URL,
	}
	if ws.Ready {
		status.Phase = tenancyv1alpha1.PhaseReady
		status.Message = ""
	}
	if err := updateTenantStatus(ctx, c, &tenant, status); err != nil {
		return ctrl.Result{}, err
	}

	if !ws.Ready {
		return requeue()
	}

	// The tenant's workspace exists now, so the first admin can finally be
	// granted. This is the second half of a write that could not be one:
	// the Membership lives in a workspace that did not exist when the
	// Tenant was created.
	if err := r.seedFirstAdmin(ctx, c, &tenant, ws.Cluster); err != nil {
		return ctrl.Result{}, err
	}

	logger.V(2).Info("tenant ready", "workspace", ws.Name, "workspaceCluster", ws.Cluster)
	return ctrl.Result{}, nil
}

// firstAdminSeededAnnotation is set on a Tenant once its first admin has
// been granted, so the grant is made only once.
const firstAdminSeededAnnotation = "tenancy.platform.kcp.io/first-admin-seeded"

// seedFirstAdmin grants spec.firstAdmin the admin role on the tenant, once.
//
// This seeds the first admin, it does not keep them. Whoever holds the
// tenant afterwards may revoke it like any other grant, and it must not
// come back, so the Tenant is marked once the grant is made.
func (r *reconcilers) seedFirstAdmin(ctx context.Context, platform client.Client, tenant *tenancyv1alpha1.Tenant, cluster string) error {
	if tenant.Spec.FirstAdmin == "" || tenant.Annotations[firstAdminSeededAnnotation] == "true" {
		return nil
	}

	c, err := clientFor(ctx, r.m.tenancy, multicluster.ClusterName(cluster))
	if err != nil {
		// The tenant tier is served by the tenancy export, which the new
		// workspace binds through its WorkspaceType. A moment after the
		// workspace is ready that binding may still be settling.
		return err
	}

	// Same shape as the CLI's grant naming, so a seeded grant and a
	// hand-made one for the same subject are the same object rather than
	// two that fight.
	name := naming.MembershipName(string(tenancyv1alpha1.SubjectKindUser), tenant.Spec.FirstAdmin, tenant.Name, "")
	membership := &tenancyv1alpha1.Membership{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: tenancyv1alpha1.MembershipSpec{
			Subject: tenancyv1alpha1.Subject{
				Kind: tenancyv1alpha1.SubjectKindUser,
				Name: tenant.Spec.FirstAdmin,
			},
			Role:   tenancyv1alpha1.RoleAdmin,
			Tenant: tenant.Name,
		},
	}
	err = c.Create(ctx, membership)
	if apierrors.IsAlreadyExists(err) {
		// The same user already has a grant on the tenant, maybe with a
		// lower role. Make it admin, or the tenant could end up with none.
		var existing tenancyv1alpha1.Membership
		if err := c.Get(ctx, client.ObjectKey{Name: name}, &existing); err != nil {
			return fmt.Errorf("get the existing grant of tenant %q's first admin: %w", tenant.Name, err)
		}
		if existing.Spec.Role != tenancyv1alpha1.RoleAdmin {
			existing.Spec.Role = tenancyv1alpha1.RoleAdmin
			if err := c.Update(ctx, &existing); err != nil {
				return fmt.Errorf("make tenant %q's first admin an admin: %w", tenant.Name, err)
			}
		}
	} else if err != nil {
		return fmt.Errorf("seed the first admin of tenant %q: %w", tenant.Name, err)
	}

	patch := client.MergeFrom(tenant.DeepCopy())
	if tenant.Annotations == nil {
		tenant.Annotations = map[string]string{}
	}
	tenant.Annotations[firstAdminSeededAnnotation] = "true"
	if err := platform.Patch(ctx, tenant, patch); err != nil {
		return fmt.Errorf("mark tenant %q's first admin as seeded: %w", tenant.Name, err)
	}
	return nil
}

func updateTenantStatus(ctx context.Context, c client.Client, tenant *tenancyv1alpha1.Tenant, status tenancyv1alpha1.TenantStatus) error {
	if tenant.Status == status {
		return nil
	}
	tenant.Status = status
	if err := c.Status().Update(ctx, tenant); err != nil && !apierrors.IsConflict(err) {
		return err
	}
	return nil
}
