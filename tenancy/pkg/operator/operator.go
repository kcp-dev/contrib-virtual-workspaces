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

// Package operator reconciles the tenancy API into kcp workspaces and RBAC.
//
// It runs one manager per APIExport, and that is the whole security design:
// each manager can only see and touch the clusters that bound its export,
// with only the verbs that export claims. Nothing here holds an admin
// client, and no write happens outside a claim.
//
//	platform     Tenants, in the platform workspace.
//	tenancy      Projects and Memberships, in each tenant workspace.
//	provisioner  creates and deletes Workspace objects, in the two tiers
//	             that have children: the platform workspace and each tenant
//	             workspace.
//	access       writes namespaces and RBAC, in project workspaces only.
//
// A Membership never materializes in the tenant workspace, only in project
// workspaces. The tenant tier holds the objects that decide who may reach
// what, so no binding there names any tenant identity — that tier is
// unreachable by construction rather than by a check.
package operator

import (
	"context"
	"fmt"
	"strings"
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

	"github.com/kcp-dev/contrib-virtual-workspaces/tenancy/pkg/bootstrap"
	"github.com/kcp-dev/contrib-virtual-workspaces/tenancy/pkg/naming"
	tenancyv1alpha1 "github.com/kcp-dev/contrib-virtual-workspaces/tenancy/sdk/apis/tenancy/v1alpha1"
)

// requeueWhilePending is how long the operator waits between looks at a
// workspace that has not become ready yet. Readiness has no watchable event
// on this side, and a fresh workspace is ready within seconds, so a short
// poll is simpler than engaging every provisioned cluster for one field.
const requeueWhilePending = 3 * time.Second

// finalizer marks objects whose materialized side effects must be removed
// before the object may go.
const finalizer = tenancyv1alpha1.GroupName + "/cleanup"

// Options configures the operator.
type Options struct {
	// RestConfig reaches the kcp shard hosting the tenancy APIExports.
	RestConfig *rest.Config
	// Slice names, one per export. Defaults match what init installs.
	PlatformSlice    string
	TenancySlice     string
	ProvisionerSlice string
	AccessSlice      string
	// Strategy names workspaces after tenants and projects.
	Strategy naming.Strategy
	// ExportsPath is where the WorkspaceTypes live; Workspace objects
	// reference their type by path.
	ExportsPath string
	// TenantsPath is the workspace every tenant workspace is created
	// under. It is deliberately not the workspace Tenant records live in,
	// so the operator resolves it once rather than deriving it from the
	// object it is reconciling.
	TenantsPath string
}

func (o *Options) defaults() {
	if o.PlatformSlice == "" {
		o.PlatformSlice = bootstrap.ExportPlatform
	}
	if o.TenancySlice == "" {
		o.TenancySlice = bootstrap.ExportTenancy
	}
	if o.ProvisionerSlice == "" {
		o.ProvisionerSlice = bootstrap.ExportProvisioner
	}
	if o.AccessSlice == "" {
		o.AccessSlice = bootstrap.ExportAccess
	}
}

// managers is the set this operator runs, one per APIExport.
type managers struct {
	platform    mcmanager.Manager
	tenancy     mcmanager.Manager
	provisioner mcmanager.Manager
	access      mcmanager.Manager
}

// Run starts the operator and blocks until ctx is cancelled.
func Run(ctx context.Context, opts Options) error {
	if opts.RestConfig == nil {
		return fmt.Errorf("rest config is required")
	}
	if opts.Strategy == nil {
		return fmt.Errorf("naming strategy is required")
	}
	if opts.ExportsPath == "" {
		return fmt.Errorf("exports path is required; Workspace objects name their type by path")
	}
	if opts.TenantsPath == "" {
		return fmt.Errorf("tenants path is required; it is the workspace tenant workspaces are created under")
	}
	opts.defaults()

	// Resolved once: a Tenant record says nothing about where its
	// workspace belongs, because the registry and the provisioning tree
	// are different tiers on purpose.
	tenantsCluster, err := resolveCluster(opts.RestConfig, opts.TenantsPath)
	if err != nil {
		return fmt.Errorf("resolve the logical cluster of %s (has `tenancy-vw init` run?): %w", opts.TenantsPath, err)
	}

	logger := log.FromContext(ctx).WithName("tenancy-operator")
	sch := buildScheme()

	m := &managers{}
	if m.platform, err = newManager(opts.RestConfig, opts.PlatformSlice, sch, logger); err != nil {
		return err
	}
	if m.tenancy, err = newManager(opts.RestConfig, opts.TenancySlice, sch, logger); err != nil {
		return err
	}
	if m.provisioner, err = newManager(opts.RestConfig, opts.ProvisionerSlice, sch, logger); err != nil {
		return err
	}
	if m.access, err = newManager(opts.RestConfig, opts.AccessSlice, sch, logger); err != nil {
		return err
	}

	r := &reconcilers{
		m:              m,
		strategy:       opts.Strategy,
		exportsPath:    opts.ExportsPath,
		tenantsCluster: multicluster.ClusterName(tenantsCluster),
		logger:         logger,
	}

	// Tenants are a platform-tier object; Projects and Memberships are a
	// tenant-tier one. Each controller runs on the manager whose export
	// actually serves the objects it watches.
	if err := mcbuilder.ControllerManagedBy(m.platform).
		Named("tenancy-tenant").
		For(&tenancyv1alpha1.Tenant{}).
		Complete(mcreconcile.Func(r.reconcileTenant)); err != nil {
		return fmt.Errorf("build tenant controller: %w", err)
	}
	if err := mcbuilder.ControllerManagedBy(m.tenancy).
		Named("tenancy-project").
		For(&tenancyv1alpha1.Project{}).
		Complete(mcreconcile.Func(r.reconcileProject)); err != nil {
		return fmt.Errorf("build project controller: %w", err)
	}
	if err := mcbuilder.ControllerManagedBy(m.tenancy).
		Named("tenancy-membership").
		For(&tenancyv1alpha1.Membership{}).
		Complete(mcreconcile.Func(r.reconcileMembership)); err != nil {
		return fmt.Errorf("build membership controller: %w", err)
	}

	// Every slice needs the same nudge: kcp computes a slice's URLs when
	// the slice object is reconciled, and a consumer binding the export is
	// not a slice event.
	for _, slice := range []string{opts.PlatformSlice, opts.TenancySlice, opts.ProvisionerSlice, opts.AccessSlice} {
		if err := m.platform.GetLocalManager().Add(manager.RunnableFunc(func(ctx context.Context) error {
			return nudgeEndpointSlice(ctx, opts.RestConfig, slice, logger)
		})); err != nil {
			return fmt.Errorf("register endpoint slice nudger for %s: %w", slice, err)
		}
	}

	logger.Info("tenancy operator running",
		"tenantStore", "(the clusters bound to "+opts.PlatformSlice+")",
		"provisioningParent", opts.TenantsPath+" ("+tenantsCluster+")",
		"platformSlice", opts.PlatformSlice,
		"tenancySlice", opts.TenancySlice,
		"provisionerSlice", opts.ProvisionerSlice,
		"accessSlice", opts.AccessSlice,
		"namingStrategy", opts.Strategy.Name())

	errs := make(chan error, 4)
	for _, mgr := range []mcmanager.Manager{m.provisioner, m.access, m.tenancy, m.platform} {
		go func(mgr mcmanager.Manager) { errs <- mgr.Start(ctx) }(mgr)
	}
	select {
	case err := <-errs:
		return err
	case <-ctx.Done():
		return nil
	}
}

func buildScheme() *runtime.Scheme {
	sch := runtime.NewScheme()
	utilruntime.Must(scheme.AddToScheme(sch))
	utilruntime.Must(corev1alpha1.AddToScheme(sch))
	utilruntime.Must(kcptenancyv1alpha1.AddToScheme(sch))
	utilruntime.Must(apisv1alpha1.AddToScheme(sch))
	utilruntime.Must(tenancyv1alpha1.AddToScheme(sch))
	return sch
}

// newManager builds a multicluster manager over one APIExport's virtual
// workspace. The export is the boundary: this manager sees exactly the
// clusters that bound it, with exactly the verbs it claims.
func newManager(cfg *rest.Config, slice string, sch *runtime.Scheme, logger logr.Logger) (mcmanager.Manager, error) {
	provider, err := apiexport.New(cfg, slice, apiexport.Options{Scheme: sch, Log: &logger})
	if err != nil {
		return nil, fmt.Errorf("construct provider for %q: %w", slice, err)
	}
	mgr, err := mcmanager.New(cfg, provider, manager.Options{
		Scheme:  sch,
		Metrics: metricsserver.Options{BindAddress: "0"},
	})
	if err != nil {
		return nil, fmt.Errorf("construct manager for %q: %w", slice, err)
	}
	return mgr, nil
}

// reconcilers carries what every reconcile needs.
type reconcilers struct {
	m           *managers
	strategy    naming.Strategy
	exportsPath string
	// tenantsCluster is the logical cluster tenant workspaces are created
	// in — the provisioning parent, not the store the Tenant was read from.
	tenantsCluster multicluster.ClusterName
	logger         logr.Logger
}

// resolveCluster turns a workspace path into its logical cluster name.
func resolveCluster(cfg *rest.Config, path string) (string, error) {
	scoped := rest.CopyConfig(cfg)
	host := scoped.Host
	if i := strings.Index(host, "/clusters/"); i >= 0 {
		host = host[:i]
	}
	scoped.Host = strings.TrimSuffix(host, "/") + "/clusters/" + path

	dyn, err := dynamic.NewForConfig(scoped)
	if err != nil {
		return "", fmt.Errorf("build dynamic client: %w", err)
	}
	lc, err := dyn.Resource(schema.GroupVersionResource{
		Group: "core.kcp.io", Version: "v1alpha1", Resource: "logicalclusters",
	}).Get(context.Background(), "cluster", metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	name := lc.GetAnnotations()["kcp.io/cluster"]
	if name == "" {
		return "", fmt.Errorf("the LogicalCluster of %s carries no kcp.io/cluster annotation", path)
	}
	return name, nil
}

// clientFor returns a client for one cluster through one export's virtual
// workspace. Which manager is passed decides what the call is allowed to
// do, which is why every caller names one explicitly.
func clientFor(ctx context.Context, mgr mcmanager.Manager, cluster multicluster.ClusterName) (client.Client, error) {
	if mgr == nil {
		return nil, fmt.Errorf("no manager for cluster %q: the export it reads through is not wired", cluster)
	}
	cl, err := mgr.GetCluster(ctx, cluster)
	if err != nil {
		return nil, fmt.Errorf("get cluster %q: %w", cluster, err)
	}
	return cl.GetClient(), nil
}

// requeue is the poll-again result used while workspaces come up.
func requeue() (ctrl.Result, error) {
	return ctrl.Result{RequeueAfter: requeueWhilePending}, nil
}

// requeueAfterStatus folds a status write into the poll-again result.
func requeueAfterStatus(err error) (ctrl.Result, error) {
	if err != nil {
		return ctrl.Result{}, err
	}
	return requeue()
}

var endpointSliceGVR = schema.GroupVersionResource{
	Group: "apis.kcp.io", Version: "v1alpha1", Resource: "apiexportendpointslices",
}

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

		patch := fmt.Sprintf(`{"metadata":{"annotations":{%q:%q}}}`, bootstrap.NudgeAnnotation, time.Now().UTC().Format(time.RFC3339))
		if _, err := dyn.Resource(endpointSliceGVR).Patch(ctx, sliceName, types.MergePatchType, []byte(patch), metav1.PatchOptions{}); err != nil {
			logger.V(2).Info("endpoint slice nudge: patch failed", "slice", sliceName, "error", err.Error())
			continue
		}
		logger.V(2).Info("nudged URL-less endpoint slice", "slice", sliceName)
	}
}
