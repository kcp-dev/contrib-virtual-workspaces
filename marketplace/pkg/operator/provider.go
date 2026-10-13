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

// reconcileProducer turns a Provider record into a workspace (for mode
// provisioned) or records the workspace the producer brought (for mode
// external). Either way the record grants nothing: reach into a producer
// workspace comes from the marketplace-publish binding there, which for
// provisioned workspaces the `producer` WorkspaceType carries and for
// external ones the producer creates themselves — that binding, not this
// object, is the consent.
func (r *reconcilers) reconcileProducer(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("cluster", req.ClusterName, "producer", req.Name)

	c, err := clientFor(ctx, r.m.catalog, req.ClusterName)
	if err != nil {
		return ctrl.Result{}, err
	}

	var producer marketplacev1alpha1.Producer
	if err := c.Get(ctx, req.NamespacedName, &producer); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !producer.DeletionTimestamp.IsZero() {
		if producer.Spec.Mode == marketplacev1alpha1.ProducerModeProvisioned {
			prov, err := clientFor(ctx, r.m.provisioner, r.producersCluster)
			if err != nil {
				return ctrl.Result{}, err
			}
			done, err := deleteWorkspace(ctx, prov, producer.Status.Workspace, string(producer.UID))
			if err != nil {
				return ctrl.Result{}, err
			}
			if !done {
				return requeue()
			}
		}
		if controllerutil.RemoveFinalizer(&producer, finalizer) {
			if err := c.Update(ctx, &producer); err != nil {
				return ctrl.Result{}, client.IgnoreNotFound(err)
			}
			logger.Info("producer cleaned up")
		}
		return ctrl.Result{}, nil
	}

	if controllerutil.AddFinalizer(&producer, finalizer) {
		if err := c.Update(ctx, &producer); err != nil {
			if apierrors.IsConflict(err) {
				return requeue()
			}
			return ctrl.Result{}, err
		}
	}

	switch producer.Spec.Mode {
	case marketplacev1alpha1.ProducerModeProvisioned:
		prov, err := clientFor(ctx, r.m.provisioner, r.producersCluster)
		if err != nil {
			return ctrl.Result{}, err
		}
		ws, err := ensureWorkspace(ctx, prov, producer.Name, string(producer.UID),
			bootstrap.WorkspaceTypeProvider, r.exportsPath)
		if err != nil {
			return ctrl.Result{}, updateProviderStatus(ctx, c, &producer, marketplacev1alpha1.ProducerStatus{
				Phase: marketplacev1alpha1.PhaseError, Message: err.Error(),
			})
		}
		status := marketplacev1alpha1.ProducerStatus{
			Phase:            marketplacev1alpha1.PhasePending,
			Message:          "waiting for the workspace to become ready",
			Workspace:        ws.Name,
			WorkspacePath:    r.producersPath + ":" + ws.Name,
			WorkspaceCluster: ws.Cluster,
			URL:              ws.URL,
		}
		if ws.Ready {
			status.Phase = marketplacev1alpha1.PhaseReady
			status.Message = ""
		}
		if err := updateProviderStatus(ctx, c, &producer, status); err != nil {
			return ctrl.Result{}, err
		}
		if !ws.Ready {
			return requeue()
		}
		logger.V(2).Info("producer workspace ready", "workspace", ws.Name, "workspaceCluster", ws.Cluster)
		return ctrl.Result{}, nil

	case marketplacev1alpha1.ProducerModeExternal:
		if producer.Spec.WorkspacePath == "" {
			return ctrl.Result{}, updateProviderStatus(ctx, c, &producer, marketplacev1alpha1.ProducerStatus{
				Phase:   marketplacev1alpha1.PhaseError,
				Message: "mode external requires spec.workspacePath: the canonical path of the producer's own workspace",
			})
		}
		// The workspace's logical cluster cannot be resolved from here —
		// the operator has no standing in an external workspace until its
		// owner binds marketplace-publish. The offering reconciler learns
		// the cluster when the first Offering arrives from a workspace
		// whose path matches, and fills it in.
		status := marketplacev1alpha1.ProducerStatus{
			WorkspacePath:    producer.Spec.WorkspacePath,
			WorkspaceCluster: producer.Status.WorkspaceCluster, // preserved, learned at publish
		}
		if status.WorkspaceCluster != "" {
			status.Phase = marketplacev1alpha1.PhaseReady
		} else {
			status.Phase = marketplacev1alpha1.PhasePending
			status.Message = "waiting for the workspace to bind marketplace-publish and publish an Offering"
		}
		return ctrl.Result{}, updateProviderStatus(ctx, c, &producer, status)

	default:
		return ctrl.Result{}, updateProviderStatus(ctx, c, &producer, marketplacev1alpha1.ProducerStatus{
			Phase:   marketplacev1alpha1.PhaseError,
			Message: "spec.mode must be provisioned or external",
		})
	}
}

func updateProviderStatus(ctx context.Context, c client.Client, producer *marketplacev1alpha1.Producer, status marketplacev1alpha1.ProducerStatus) error {
	if producer.Status == status {
		return nil
	}
	producer.Status = status
	if err := c.Status().Update(ctx, producer); err != nil && !apierrors.IsConflict(err) {
		return err
	}
	return nil
}
