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

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/kcp-dev/contrib-virtual-workspaces/tenancy/pkg/bootstrap"
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
	logger.V(2).Info("tenant ready", "workspace", ws.Name, "workspaceCluster", ws.Cluster)
	return ctrl.Result{}, nil
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
