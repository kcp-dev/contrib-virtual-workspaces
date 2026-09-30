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

// Package operator reconciles the marketplace API into provider
// workspaces and bind grants.
//
// It runs one manager per APIExport with schemas, and holds one client for
// the provisioner capability — and that is the whole security design: each
// write happens through exactly one claim someone accepted.
//
//	catalog      Providers, in the catalog workspace.
//	publish      APIExports, in each provider workspace; materializes the
//	             per-export `bind` grant that publishing means. It reads
//	             those exports through a claim and never writes to them —
//	             an offering is a provider's own object, annotated.
//	provisioner  (client only) creates provider workspaces under the
//	             providers parent.
//
// The operator never touches a consumer workspace: enabling an offering is
// a plain kcp APIBinding the consumer creates themselves, authorized by
// their own RBAC and by the `bind` grant publishing materialized.
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
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"

	"github.com/kcp-dev/multicluster-provider/apiexport"
	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	kcptenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"

	"github.com/kcp-dev/contrib-virtual-workspaces/marketplace/pkg/bootstrap"
	marketplacev1alpha1 "github.com/kcp-dev/contrib-virtual-workspaces/marketplace/sdk/apis/marketplace/v1alpha1"
)

// requeueWhilePending is how long the operator waits between looks at
// something that has not become ready yet.
const requeueWhilePending = 3 * time.Second

// finalizer marks objects whose materialized side effects must be removed
// before the object may go. It is used only on Provider records, which the
// marketplace owns — never on a provider's own APIExport.
const finalizer = marketplacev1alpha1.GroupName + "/cleanup"

// requeueWhileUnregistered is the slower beat for a published export whose
// workspace no Provider record names. Registering one happens in another
// workspace, so no event here will report it — but it is a human action,
// not a controller's, so looking every few seconds would be noise.
const requeueWhileUnregistered = 30 * time.Second

// Options configures the operator.
type Options struct {
	// RestConfig reaches the kcp shard hosting the marketplace APIExports.
	RestConfig *rest.Config
	// Slice names, one per export. Defaults match what init installs.
	CatalogSlice     string
	PublishSlice     string
	ProvisionerSlice string
	// ExportsPath is where the WorkspaceTypes live; Workspace objects
	// reference their type by path.
	ExportsPath string
	// CatalogPath is the workspace Provider records live in.
	CatalogPath string
	// ProvidersPath is the workspace provisioned provider workspaces are
	// created under.
	ProvidersPath string
	// BindUsers and BindGroups are the subjects the per-offering `bind`
	// grants name: who may enable a published offering. Both empty means
	// the system:authenticated group — mirroring this repo's bind-RBAC
	// stance that a claim only takes effect once the consumer accepts it,
	// and matching what kcp does for its own root exports.
	BindUsers  []string
	BindGroups []string
}

func (o *Options) defaults() {
	if o.CatalogSlice == "" {
		o.CatalogSlice = bootstrap.ExportCatalog
	}
	if o.PublishSlice == "" {
		o.PublishSlice = bootstrap.ExportPublish
	}
	if o.ProvisionerSlice == "" {
		o.ProvisionerSlice = bootstrap.ExportProvisioner
	}
}

// managers is the set this operator runs. provisioner has no controllers —
// it exists to hand out per-cluster clients scoped to its claim.
type managers struct {
	catalog     mcmanager.Manager
	publish     mcmanager.Manager
	provisioner mcmanager.Manager
}

// Run starts the operator and blocks until ctx is cancelled.
func Run(ctx context.Context, opts Options) error {
	if opts.RestConfig == nil {
		return fmt.Errorf("rest config is required")
	}
	if opts.ExportsPath == "" {
		return fmt.Errorf("exports path is required; Workspace objects name their type by path")
	}
	if opts.CatalogPath == "" {
		return fmt.Errorf("catalog path is required; it is the workspace Provider records live in")
	}
	if opts.ProvidersPath == "" {
		return fmt.Errorf("providers path is required; it is the workspace provider workspaces are created under")
	}
	opts.defaults()

	catalogCluster, err := resolveCluster(opts.RestConfig, opts.CatalogPath)
	if err != nil {
		return fmt.Errorf("resolve the logical cluster of %s (has `marketplace-vw init` run?): %w", opts.CatalogPath, err)
	}
	providersCluster, err := resolveCluster(opts.RestConfig, opts.ProvidersPath)
	if err != nil {
		return fmt.Errorf("resolve the logical cluster of %s (has `marketplace-vw init` run?): %w", opts.ProvidersPath, err)
	}

	bindUsers, bindGroups := opts.BindUsers, opts.BindGroups
	if len(bindUsers)+len(bindGroups) == 0 {
		bindGroups = []string{"system:authenticated"}
	}

	logger := log.FromContext(ctx).WithName("marketplace-operator")
	sch := buildScheme()

	m := &managers{}
	if m.catalog, err = newManager(opts.RestConfig, opts.CatalogSlice, sch, logger); err != nil {
		return err
	}
	if m.publish, err = newManager(opts.RestConfig, opts.PublishSlice, sch, logger); err != nil {
		return err
	}
	if m.provisioner, err = newManager(opts.RestConfig, opts.ProvisionerSlice, sch, logger); err != nil {
		return err
	}

	r := &reconcilers{
		m:                m,
		exportsPath:      opts.ExportsPath,
		providersPath:    opts.ProvidersPath,
		catalogCluster:   multicluster.ClusterName(catalogCluster),
		providersCluster: multicluster.ClusterName(providersCluster),
		bindUsers:        bindUsers,
		bindGroups:       bindGroups,
		logger:           logger,
	}

	if err := mcbuilder.ControllerManagedBy(m.catalog).
		Named("marketplace-provider").
		For(&marketplacev1alpha1.Provider{}).
		Complete(mcreconcile.Func(r.reconcileProvider)); err != nil {
		return fmt.Errorf("build provider controller: %w", err)
	}
	// Every APIExport in every provider workspace comes through here,
	// because the annotation that publishes one is on the export itself:
	// there is no narrower thing to watch, and watching the exports is how
	// un-publishing is noticed at all.
	if err := mcbuilder.ControllerManagedBy(m.publish).
		Named("marketplace-publish").
		For(&apisv1alpha2.APIExport{}).
		Complete(mcreconcile.Func(r.reconcileAPIExport)); err != nil {
		return fmt.Errorf("build publish controller: %w", err)
	}
	logger.Info("marketplace operator running",
		"catalog", opts.CatalogPath+" ("+catalogCluster+")",
		"providersParent", opts.ProvidersPath+" ("+providersCluster+")",
		"bindUsers", bindUsers, "bindGroups", bindGroups)

	errs := make(chan error, 3)
	for _, mgr := range []mcmanager.Manager{m.provisioner, m.publish, m.catalog} {
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
	utilruntime.Must(apisv1alpha2.AddToScheme(sch))
	utilruntime.Must(marketplacev1alpha1.AddToScheme(sch))
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
	m             *managers
	exportsPath   string
	providersPath string
	// catalogCluster is where Provider records live; providersCluster is
	// where provisioned provider workspaces are created.
	catalogCluster   multicluster.ClusterName
	providersCluster multicluster.ClusterName
	// bindUsers/bindGroups are the subjects the per-offering bind grants
	// name: who may enable a published offering.
	bindUsers  []string
	bindGroups []string
	logger     logr.Logger
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

// requeue is the poll-again result used while things come up.
func requeue() (ctrl.Result, error) {
	return ctrl.Result{RequeueAfter: requeueWhilePending}, nil
}
