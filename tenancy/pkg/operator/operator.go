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

// Package operator reconciles the tenancy API — Tenant, Project and
// Membership objects in organization workspaces — into kcp workspaces and
// RBAC.
//
// The operator discovers organization workspaces through the tenancy
// APIExport's endpoint slice (multicluster-runtime with the apiexport
// provider), the same pattern the access virtual workspace uses to watch
// RBAC. It touches two kinds of target:
//
//   - the organization workspace itself, through the apiexport virtual
//     workspace, for the tenancy objects, their status, and the tenant
//     Workspace objects (a permission claim on tenancy.kcp.io workspaces);
//   - the provisioned tenant and project workspaces, addressed directly by
//     the URL kcp reports on the Workspace object, for nested workspaces
//     and RBAC. The operator's kubeconfig identity must be authorized
//     there; in practice the operator runs as an administrator of the
//     subtree the organization workspaces live in.
package operator

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"

	"github.com/kcp-dev/multicluster-provider/apiexport"
	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	kcptenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"

	"github.com/kcp-dev/contrib-virtual-workspaces/tenancy/pkg/naming"
	tenancyv1alpha1 "github.com/kcp-dev/contrib-virtual-workspaces/tenancy/sdk/apis/tenancy/v1alpha1"
)

// requeueWhilePending is how long the operator waits between looks at a
// workspace that has not become ready yet. Workspace readiness has no
// watchable event in the org workspace beyond the object itself, and a
// fresh workspace is ready within seconds, so a short poll is simpler than
// engaging every provisioned cluster just to watch one phase field.
const requeueWhilePending = 3 * time.Second

// finalizer marks objects whose materialized side effects (a workspace, a
// binding) must be removed before the object may go.
const finalizer = tenancyv1alpha1.GroupName + "/cleanup"

// Options configures the operator.
type Options struct {
	// RestConfig reaches the kcp shard hosting the tenancy APIExport.
	RestConfig *rest.Config
	// APIExportEndpointSlice is the endpoint slice of the tenancy
	// APIExport; its URLs are where organization workspaces are found.
	APIExportEndpointSlice string
	// Strategy names workspaces after tenants and projects.
	Strategy naming.Strategy
}

// Run starts the operator and blocks until ctx is cancelled.
func Run(ctx context.Context, opts Options) error {
	if opts.RestConfig == nil {
		return fmt.Errorf("rest config is required")
	}
	if opts.APIExportEndpointSlice == "" {
		return fmt.Errorf("apiexport endpoint slice is required")
	}
	if opts.Strategy == nil {
		return fmt.Errorf("naming strategy is required")
	}

	logger := log.FromContext(ctx).WithName("tenancy-operator")

	sch := runtime.NewScheme()
	utilruntime.Must(scheme.AddToScheme(sch))
	utilruntime.Must(corev1alpha1.AddToScheme(sch))
	utilruntime.Must(kcptenancyv1alpha1.AddToScheme(sch))
	utilruntime.Must(apisv1alpha1.AddToScheme(sch))
	utilruntime.Must(tenancyv1alpha1.AddToScheme(sch))

	provider, err := apiexport.New(opts.RestConfig, opts.APIExportEndpointSlice, apiexport.Options{
		Scheme: sch,
		Log:    &logger,
	})
	if err != nil {
		return fmt.Errorf("construct apiexport provider: %w", err)
	}

	mgr, err := mcmanager.New(opts.RestConfig, provider, manager.Options{
		Scheme:  sch,
		Metrics: metricsserver.Options{BindAddress: "0"},
	})
	if err != nil {
		return fmt.Errorf("construct multicluster manager: %w", err)
	}

	r := &reconcilers{
		mgr:      mgr,
		strategy: opts.Strategy,
		base:     opts.RestConfig,
		scheme:   sch,
		logger:   logger,
	}

	if err := mcbuilder.ControllerManagedBy(mgr).
		Named("tenancy-tenant").
		For(&tenancyv1alpha1.Tenant{}).
		Complete(mcreconcile.Func(r.reconcileTenant)); err != nil {
		return fmt.Errorf("build tenant controller: %w", err)
	}
	if err := mcbuilder.ControllerManagedBy(mgr).
		Named("tenancy-project").
		For(&tenancyv1alpha1.Project{}).
		Complete(mcreconcile.Func(r.reconcileProject)); err != nil {
		return fmt.Errorf("build project controller: %w", err)
	}
	if err := mcbuilder.ControllerManagedBy(mgr).
		Named("tenancy-membership").
		For(&tenancyv1alpha1.Membership{}).
		Complete(mcreconcile.Func(r.reconcileMembership)); err != nil {
		return fmt.Errorf("build membership controller: %w", err)
	}

	// kcp's endpoint-slice URLs controller computes shard URLs when the
	// slice object is reconciled, but a consumer binding the export later
	// produces no slice event — a slice created before its first consumer
	// then stays URL-less forever, and this operator never discovers any
	// organization workspace. Until that is fixed upstream, nudge the
	// slice while it has no endpoints: any write triggers the reconcile
	// that fills the URLs, and once they exist the nudge stops.
	if err := mgr.GetLocalManager().Add(manager.RunnableFunc(func(ctx context.Context) error {
		return nudgeEndpointSlice(ctx, opts.RestConfig, opts.APIExportEndpointSlice, logger)
	})); err != nil {
		return fmt.Errorf("register endpoint slice nudger: %w", err)
	}

	logger.Info("tenancy operator running",
		"apiExportEndpointSlice", opts.APIExportEndpointSlice,
		"namingStrategy", opts.Strategy.Name())

	return mgr.Start(ctx)
}

var endpointSliceGVR = schema.GroupVersionResource{
	Group: "apis.kcp.io", Version: "v1alpha1", Resource: "apiexportendpointslices",
}

// nudgeAnnotation is bumped on the endpoint slice while it has no
// endpoints; the write is what re-triggers kcp's URLs controller.
const nudgeAnnotation = tenancyv1alpha1.GroupName + "/nudged-at"

func nudgeEndpointSlice(ctx context.Context, cfg *rest.Config, sliceName string, logger logr.Logger) error {
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("build dynamic client: %w", err)
	}

	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}

		slice, err := dyn.Resource(endpointSliceGVR).Get(ctx, sliceName, metav1.GetOptions{})
		if err != nil {
			logger.V(2).Info("endpoint slice nudge: get failed", "slice", sliceName, "error", err.Error())
			continue
		}
		endpoints, _, _ := unstructured.NestedSlice(slice.Object, "status", "endpoints")
		if len(endpoints) > 0 {
			continue
		}

		patch := fmt.Sprintf(`{"metadata":{"annotations":{%q:%q}}}`, nudgeAnnotation, time.Now().UTC().Format(time.RFC3339))
		if _, err := dyn.Resource(endpointSliceGVR).Patch(ctx, sliceName, types.MergePatchType, []byte(patch), metav1.PatchOptions{}); err != nil {
			logger.V(2).Info("endpoint slice nudge: patch failed", "slice", sliceName, "error", err.Error())
			continue
		}
		logger.V(2).Info("nudged URL-less endpoint slice", "slice", sliceName)
	}
}

// reconcilers carries what every reconcile needs.
type reconcilers struct {
	mgr      mcmanager.Manager
	strategy naming.Strategy
	base     *rest.Config
	scheme   *runtime.Scheme
	logger   logr.Logger
}

// clusterClient returns the client for one engaged organization workspace.
func (r *reconcilers) clusterClient(ctx context.Context, clusterName multicluster.ClusterName) (client.Client, error) {
	cl, err := r.mgr.GetCluster(ctx, clusterName)
	if err != nil {
		return nil, fmt.Errorf("get cluster %q: %w", clusterName, err)
	}
	return cl.GetClient(), nil
}

// directClient returns a client addressing a workspace by the absolute URL
// kcp reported for it, authenticated as the operator's own kubeconfig
// identity. Used for everything inside provisioned tenant and project
// workspaces, which are not consumers of the tenancy APIExport.
func (r *reconcilers) directClient(workspaceURL string) (client.Client, error) {
	cfg := rest.CopyConfig(r.base)
	cfg.Host = workspaceURL
	return client.New(cfg, client.Options{Scheme: r.scheme})
}

// result is shorthand for the poll-again result used while workspaces come up.
func requeue() (ctrl.Result, error) {
	return ctrl.Result{RequeueAfter: requeueWhilePending}, nil
}
