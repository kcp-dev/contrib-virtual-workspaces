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

	"github.com/kcp-dev/contrib-virtual-workspaces/marketplace/pkg/bootstrap"
	marketplacev1alpha1 "github.com/kcp-dev/contrib-virtual-workspaces/marketplace/sdk/apis/marketplace/v1alpha1"
)

// reconcileProvider turns a Provider record into a workspace (for mode
// provisioned) or records the workspace the provider brought (for mode
// external). Either way the record grants nothing: reach into a provider
// workspace comes from the marketplace-publish binding there, which for
// provisioned workspaces the `provider` WorkspaceType carries and for
// external ones the provider creates themselves — that binding, not this
// object, is the consent.
func (r *reconcilers) reconcileProvider(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("cluster", req.ClusterName, "provider", req.Name)

	c, err := clientFor(ctx, r.m.catalog, req.ClusterName)
	if err != nil {
		return ctrl.Result{}, err
	}

	var provider marketplacev1alpha1.Provider
	if err := c.Get(ctx, req.NamespacedName, &provider); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !provider.DeletionTimestamp.IsZero() {
		if provider.Spec.Mode == marketplacev1alpha1.ProviderModeProvisioned {
			prov, err := clientFor(ctx, r.m.provisioner, r.providersCluster)
			if err != nil {
				return ctrl.Result{}, err
			}
			done, err := deleteWorkspace(ctx, prov, provider.Status.Workspace, string(provider.UID))
			if err != nil {
				return ctrl.Result{}, err
			}
			if !done {
				return requeue()
			}
		}
		if controllerutil.RemoveFinalizer(&provider, finalizer) {
			if err := c.Update(ctx, &provider); err != nil {
				return ctrl.Result{}, client.IgnoreNotFound(err)
			}
			logger.Info("provider cleaned up")
		}
		return ctrl.Result{}, nil
	}

	if controllerutil.AddFinalizer(&provider, finalizer) {
		if err := c.Update(ctx, &provider); err != nil {
			if apierrors.IsConflict(err) {
				return requeue()
			}
			return ctrl.Result{}, err
		}
	}

	switch provider.Spec.Mode {
	case marketplacev1alpha1.ProviderModeProvisioned:
		prov, err := clientFor(ctx, r.m.provisioner, r.providersCluster)
		if err != nil {
			return ctrl.Result{}, err
		}
		ws, err := ensureWorkspace(ctx, prov, provider.Name, string(provider.UID),
			bootstrap.WorkspaceTypeProvider, r.exportsPath)
		if err != nil {
			return ctrl.Result{}, updateProviderStatus(ctx, c, &provider, marketplacev1alpha1.ProviderStatus{
				Phase: marketplacev1alpha1.PhaseError, Message: err.Error(),
			})
		}
		status := marketplacev1alpha1.ProviderStatus{
			Phase:            marketplacev1alpha1.PhasePending,
			Message:          "waiting for the workspace to become ready",
			Workspace:        ws.Name,
			WorkspacePath:    r.providersPath + ":" + ws.Name,
			WorkspaceCluster: ws.Cluster,
			URL:              ws.URL,
		}
		if ws.Ready {
			status.Phase = marketplacev1alpha1.PhaseReady
			status.Message = ""
		}
		if err := updateProviderStatus(ctx, c, &provider, status); err != nil {
			return ctrl.Result{}, err
		}
		if !ws.Ready {
			return requeue()
		}
		logger.V(2).Info("provider workspace ready", "workspace", ws.Name, "workspaceCluster", ws.Cluster)
		return ctrl.Result{}, nil

	case marketplacev1alpha1.ProviderModeExternal:
		if provider.Spec.WorkspacePath == "" {
			return ctrl.Result{}, updateProviderStatus(ctx, c, &provider, marketplacev1alpha1.ProviderStatus{
				Phase:   marketplacev1alpha1.PhaseError,
				Message: "mode external requires spec.workspacePath: the canonical path of the provider's own workspace",
			})
		}
		// The workspace's logical cluster cannot be resolved from here —
		// the operator has no standing in an external workspace until its
		// owner binds marketplace-publish. The offering reconciler learns
		// the cluster when the first Offering arrives from a workspace
		// whose path matches, and fills it in.
		status := marketplacev1alpha1.ProviderStatus{
			WorkspacePath:    provider.Spec.WorkspacePath,
			WorkspaceCluster: provider.Status.WorkspaceCluster, // preserved, learned at publish
		}
		if status.WorkspaceCluster != "" {
			status.Phase = marketplacev1alpha1.PhaseReady
		} else {
			status.Phase = marketplacev1alpha1.PhasePending
			status.Message = "waiting for the workspace to bind marketplace-publish and publish an Offering"
		}
		return ctrl.Result{}, updateProviderStatus(ctx, c, &provider, status)

	default:
		return ctrl.Result{}, updateProviderStatus(ctx, c, &provider, marketplacev1alpha1.ProviderStatus{
			Phase:   marketplacev1alpha1.PhaseError,
			Message: "spec.mode must be provisioned or external",
		})
	}
}

func updateProviderStatus(ctx context.Context, c client.Client, provider *marketplacev1alpha1.Provider, status marketplacev1alpha1.ProviderStatus) error {
	if provider.Status == status {
		return nil
	}
	provider.Status = status
	if err := c.Status().Update(ctx, provider); err != nil && !apierrors.IsConflict(err) {
		return err
	}
	return nil
}
