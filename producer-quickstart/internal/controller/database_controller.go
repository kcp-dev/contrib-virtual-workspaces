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

// Package controller reconciles Databases in every workspace that enabled
// the export.
//
// The controller does not know those workspaces; the multicluster manager
// does. It follows the export's APIExportEndpointSlice, and every workspace
// that binds the export shows up as a "cluster" — one request names both the
// cluster and the object, and the client it hands back is scoped to that
// workspace. A producer never lists workspaces, and never holds a credential
// to anything but its own export.
package controller

import (
	"context"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	dbaasv1alpha1 "example.com/dbaas-producer/apis/v1alpha1"
)

// provisioningDelay stands in for the time real provisioning takes. The
// point of keeping a Pending pass at all is that the consumer sees progress
// — a Database that goes straight to Ready teaches nothing about how the
// console and kubectl show status, which is half of what this template is
// for.
const provisioningDelay = 2 * time.Second

// Reconciler is the multicluster side: it resolves the request's cluster to
// a client and hands the rest to Reconcile.
type Reconciler struct {
	manager mcmanager.Manager
}

// SetupWithManager registers the controller. One controller, watching one
// kind, across every workspace the provider discovers.
func (r *Reconciler) SetupWithManager(mgr mcmanager.Manager) error {
	r.manager = mgr
	// GenerationChangedPredicate: a status write bumps resourceVersion but
	// not generation, so without it every status update the controller
	// makes comes straight back as an event — and the Pending pass below
	// would be followed by the Ready pass immediately, never waiting.
	return mcbuilder.ControllerManagedBy(mgr).
		Named("database").
		For(&dbaasv1alpha1.Database{}, mcbuilder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Complete(r)
}

// Reconcile implements mcreconcile.Reconciler.
func (r *Reconciler) Reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	cl, err := r.manager.GetCluster(ctx, req.ClusterName)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("get cluster %q: %w", req.ClusterName, err)
	}
	ctx = log.IntoContext(ctx, log.FromContext(ctx).WithValues("cluster", req.ClusterName))
	return Reconcile(ctx, cl.GetClient(), req.NamespacedName)
}

// Reconcile is the single-workspace logic: given a client for one workspace
// and the name of a Database in it, bring the status up to date with the
// spec. It is a plain function over client.Client so a test can drive it
// with a fake, with no kcp and no multicluster machinery.
//
// Replace the body with real provisioning. What should survive the
// replacement: observedGeneration (so a spec change is noticed), the
// Pending/Ready phases with a condition (so every client can tell what is
// going on), and returning RequeueAfter rather than sleeping.
func Reconcile(ctx context.Context, c client.Client, key types.NamespacedName) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("database", key.Name)

	var db dbaasv1alpha1.Database
	if err := c.Get(ctx, key, &db); err != nil {
		// Gone is fine: nothing to do for an object that no longer exists.
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Deletion. Nothing external exists for this template, so there is no
	// finalizer; a real producer that provisions something adds one on
	// first sight (controllerutil.AddFinalizer) and tears down here before
	// removing it.
	if !db.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}

	// Already current: the status describes this generation and is final.
	if db.Status.ObservedGeneration == db.Generation && db.Status.Phase == dbaasv1alpha1.PhaseReady {
		return ctrl.Result{}, nil
	}

	// First pass for this generation: acknowledge, then come back.
	if db.Status.ObservedGeneration != db.Generation || db.Status.Phase != dbaasv1alpha1.PhasePending {
		db.Status.ObservedGeneration = db.Generation
		db.Status.Phase = dbaasv1alpha1.PhasePending
		db.Status.Endpoint = ""
		db.Status.Message = fmt.Sprintf("provisioning a %s %s instance", db.Spec.Size, db.Spec.Engine)
		setReady(&db, metav1.ConditionFalse, "Provisioning", db.Status.Message)
		if err := c.Status().Update(ctx, &db); err != nil {
			return ctrl.Result{}, fmt.Errorf("update status: %w", err)
		}
		logger.Info("provisioning", "engine", db.Spec.Engine, "size", db.Spec.Size)
		return ctrl.Result{RequeueAfter: provisioningDelay}, nil
	}

	// Second pass: it is "up". The endpoint is where the real one would be
	// reported — a connection string, a Secret name, whatever the engine
	// hands back.
	db.Status.Phase = dbaasv1alpha1.PhaseReady
	db.Status.Endpoint = endpointFor(&db)
	db.Status.Message = ""
	setReady(&db, metav1.ConditionTrue, "Provisioned", "the database is ready")
	if err := c.Status().Update(ctx, &db); err != nil {
		return ctrl.Result{}, fmt.Errorf("update status: %w", err)
	}
	logger.Info("ready", "endpoint", db.Status.Endpoint)
	return ctrl.Result{}, nil
}

func endpointFor(db *dbaasv1alpha1.Database) string {
	port := 5432
	if db.Spec.Engine == "mysql" {
		port = 3306
	}
	return fmt.Sprintf("%s://%s.dbaas.example.com:%d", db.Spec.Engine, db.Name, port)
}

func setReady(db *dbaasv1alpha1.Database, status metav1.ConditionStatus, reason, message string) {
	cond := metav1.Condition{
		Type:               dbaasv1alpha1.ConditionReady,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: db.Generation,
		LastTransitionTime: metav1.Now(),
	}
	for i := range db.Status.Conditions {
		if db.Status.Conditions[i].Type != cond.Type {
			continue
		}
		if db.Status.Conditions[i].Status == cond.Status {
			cond.LastTransitionTime = db.Status.Conditions[i].LastTransitionTime
		}
		db.Status.Conditions[i] = cond
		return
	}
	db.Status.Conditions = append(db.Status.Conditions, cond)
}
