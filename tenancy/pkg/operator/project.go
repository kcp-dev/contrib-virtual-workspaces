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
	"k8s.io/apimachinery/pkg/types"

	tenancyv1alpha1 "github.com/kcp-dev/contrib-virtual-workspaces/tenancy/sdk/apis/tenancy/v1alpha1"
)

func (r *reconcilers) reconcileProject(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("cluster", req.ClusterName, "project", req.Name)

	c, err := r.clusterClient(ctx, req.ClusterName)
	if err != nil {
		return ctrl.Result{}, err
	}

	var project tenancyv1alpha1.Project
	if err := c.Get(ctx, req.NamespacedName, &project); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// The project's workspace lives inside the tenant's workspace, so both
	// creation and deletion go through the tenant.
	var tenant tenancyv1alpha1.Tenant
	tenantErr := c.Get(ctx, types.NamespacedName{Name: project.Spec.Tenant}, &tenant)

	if !project.DeletionTimestamp.IsZero() {
		// If the tenant or its workspace is already gone, the project's
		// workspace went with it; there is nothing left to delete.
		if tenantErr == nil && tenant.DeletionTimestamp.IsZero() && tenant.Status.URL != "" {
			parent, err := r.directClient(tenant.Status.URL)
			if err != nil {
				return ctrl.Result{}, err
			}
			done, err := deleteWorkspace(ctx, parent, project.Status.Workspace, string(project.UID))
			if err != nil {
				return ctrl.Result{}, err
			}
			if !done {
				return requeue()
			}
		}
		if controllerutil.RemoveFinalizer(&project, finalizer) {
			if err := c.Update(ctx, &project); err != nil {
				return ctrl.Result{}, client.IgnoreNotFound(err)
			}
			logger.Info("project cleaned up")
		}
		return ctrl.Result{}, nil
	}

	if controllerutil.AddFinalizer(&project, finalizer) {
		if err := c.Update(ctx, &project); err != nil {
			if apierrors.IsConflict(err) {
				return requeue()
			}
			return ctrl.Result{}, err
		}
	}

	if apierrors.IsNotFound(tenantErr) {
		return requeue2(updateProjectStatus(ctx, c, &project, tenancyv1alpha1.ProjectStatus{
			Phase:   tenancyv1alpha1.PhaseError,
			Message: fmt.Sprintf("tenant %q not found in this workspace", project.Spec.Tenant),
		}))
	}
	if tenantErr != nil {
		return ctrl.Result{}, tenantErr
	}
	if tenant.Status.Phase != tenancyv1alpha1.PhaseReady || tenant.Status.URL == "" {
		return requeue2(updateProjectStatus(ctx, c, &project, tenancyv1alpha1.ProjectStatus{
			Phase:   tenancyv1alpha1.PhasePending,
			Message: fmt.Sprintf("waiting for tenant %q workspace", project.Spec.Tenant),
		}))
	}

	parent, err := r.directClient(tenant.Status.URL)
	if err != nil {
		return ctrl.Result{}, err
	}

	prov, err := ensureWorkspace(ctx, parent,
		project.Spec.DisplayName, string(project.UID),
		r.strategy.Propose(project.Spec.DisplayName, string(project.UID)))
	if err != nil {
		if statusErr := updateProjectStatus(ctx, c, &project, tenancyv1alpha1.ProjectStatus{
			Phase:   tenancyv1alpha1.PhaseError,
			Message: err.Error(),
		}); statusErr != nil {
			logger.Error(statusErr, "writing error status")
		}
		return ctrl.Result{}, err
	}

	status := tenancyv1alpha1.ProjectStatus{
		Phase:            tenancyv1alpha1.PhasePending,
		Message:          "waiting for the workspace to become ready",
		Workspace:        prov.Name,
		WorkspaceCluster: prov.Cluster,
		URL:              prov.URL,
	}
	if prov.Ready {
		status.Phase = tenancyv1alpha1.PhaseReady
		status.Message = ""
	}
	if err := updateProjectStatus(ctx, c, &project, status); err != nil {
		return ctrl.Result{}, err
	}

	if !prov.Ready {
		return requeue()
	}
	logger.V(2).Info("project ready", "workspace", prov.Name, "workspaceCluster", prov.Cluster)
	return ctrl.Result{}, nil
}

// requeue2 folds a status write into the poll-again result.
func requeue2(err error) (ctrl.Result, error) {
	if err != nil {
		return ctrl.Result{}, err
	}
	return requeue()
}

func updateProjectStatus(ctx context.Context, c client.Client, project *tenancyv1alpha1.Project, status tenancyv1alpha1.ProjectStatus) error {
	if project.Status == status {
		return nil
	}
	project.Status = status
	if err := c.Status().Update(ctx, project); err != nil && !apierrors.IsConflict(err) {
		return err
	}
	return nil
}
