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

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"

	"github.com/kcp-dev/contrib-virtual-workspaces/tenancy/pkg/bootstrap"
	tenancyv1alpha1 "github.com/kcp-dev/contrib-virtual-workspaces/tenancy/sdk/apis/tenancy/v1alpha1"
)

// A Project lives in its tenant's own workspace, so the workspace it asks
// for is a child of the cluster this reconcile is already in. There is no
// tenant to look up and no URL to follow — which is the point of putting
// the tenant tier between the platform and its projects.
func (r *reconcilers) reconcileProject(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("cluster", req.ClusterName, "project", req.Name)

	c, err := clientFor(ctx, r.m.tenancy, req.ClusterName)
	if err != nil {
		return ctrl.Result{}, err
	}
	prov, err := clientFor(ctx, r.m.provisioner, req.ClusterName)
	if err != nil {
		return ctrl.Result{}, err
	}

	var project tenancyv1alpha1.Project
	if err := c.Get(ctx, req.NamespacedName, &project); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !project.DeletionTimestamp.IsZero() {
		done, err := deleteWorkspace(ctx, prov, project.Status.Workspace, string(project.UID))
		if err != nil {
			return ctrl.Result{}, err
		}
		if !done {
			return requeue()
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

	ws, err := ensureWorkspace(ctx, prov,
		project.Spec.DisplayName, string(project.UID),
		r.strategy.Propose(project.Spec.DisplayName, string(project.UID)),
		bootstrap.WorkspaceTypeProject, r.exportsPath)
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
		Workspace:        ws.Name,
		WorkspaceCluster: ws.Cluster,
		URL:              ws.URL,
	}
	if ws.Ready {
		status.Phase = tenancyv1alpha1.PhaseReady
		status.Message = ""
	}
	if err := updateProjectStatus(ctx, c, &project, status); err != nil {
		return ctrl.Result{}, err
	}
	if !ws.Ready {
		return requeue()
	}

	// The `project` WorkspaceType omits `extend: root:universal` so that a
	// tenant's API surface stays theirs, and the cost of that is kcp not
	// creating `default`. Creating it is exactly what the namespaces claim
	// on tenancy-access is for.
	if err := ensureDefaultNamespace(ctx, r, multicluster.ClusterName(ws.Cluster)); err != nil {
		logger.Error(err, "creating the default namespace", "workspaceCluster", ws.Cluster)
		return requeue()
	}

	// A tenant-wide grant fans out across the projects that exist when the
	// Membership is reconciled, so a project created afterwards would never
	// receive it — an authorization system silently failing to apply a
	// grant. The project pulls what applies to it rather than every
	// Membership watching for new projects.
	if err := r.applyMembershipsTo(ctx, c, project.Name, multicluster.ClusterName(ws.Cluster)); err != nil {
		logger.Error(err, "applying existing grants to the new project", "workspaceCluster", ws.Cluster)
		return requeue()
	}

	logger.V(2).Info("project ready", "workspace", ws.Name, "workspaceCluster", ws.Cluster)
	return ctrl.Result{}, nil
}

// applyMembershipsTo materializes every grant in this tenant that covers
// the named project: the tenant-wide ones, and any scoped to it. Applying
// is idempotent, so a grant already there costs a no-op.
func (r *reconcilers) applyMembershipsTo(ctx context.Context, tenantWS client.Client, projectName string, cluster multicluster.ClusterName) error {
	var memberships tenancyv1alpha1.MembershipList
	if err := tenantWS.List(ctx, &memberships); err != nil {
		return fmt.Errorf("list memberships: %w", err)
	}

	var target client.Client
	for i := range memberships.Items {
		m := &memberships.Items[i]
		if !m.DeletionTimestamp.IsZero() {
			continue
		}
		if m.Spec.Project != "" && m.Spec.Project != projectName {
			continue
		}
		if target == nil {
			var err error
			if target, err = clientFor(ctx, r.m.access, cluster); err != nil {
				return err
			}
		}
		if err := applyRBAC(ctx, target, m); err != nil {
			return fmt.Errorf("apply membership %q: %w", m.Name, err)
		}
	}
	return nil
}

// ensureDefaultNamespace creates `default` in a project workspace, through
// the access export.
func ensureDefaultNamespace(ctx context.Context, r *reconcilers, cluster multicluster.ClusterName) error {
	acc, err := clientFor(ctx, r.m.access, cluster)
	if err != nil {
		return err
	}
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default"}}
	if err := acc.Create(ctx, ns); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create the default namespace: %w", err)
	}
	return nil
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
