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

// Package provider populates the marketplace directory: Providers from the
// catalog tier's export, offerings from the APIExports published in each
// producer workspace — the same multicluster discovery the operator runs,
// but read-only into the in-memory directory the virtual workspace answers
// from.
package provider

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"

	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/kcp-dev/multicluster-provider/apiexport"
	"github.com/kcp-dev/multicluster-provider/pkg/handlers"
	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"

	"github.com/kcp-dev/contrib-virtual-workspaces/marketplace/pkg/bootstrap"
	"github.com/kcp-dev/contrib-virtual-workspaces/marketplace/pkg/directory"
	"github.com/kcp-dev/contrib-virtual-workspaces/marketplace/pkg/offering"
	marketplacev1alpha1 "github.com/kcp-dev/contrib-virtual-workspaces/marketplace/sdk/apis/marketplace/v1alpha1"
)

// Options configures the directory producer.
type Options struct {
	// RestConfig reaches the kcp shard hosting the marketplace APIExports.
	RestConfig *rest.Config
	// CatalogSlice serves Providers; PublishSlice reaches the APIExports
	// in producer workspaces.
	CatalogSlice string
	PublishSlice string
}

// Run fills dir until ctx is cancelled, marking it ready only once both
// discoveries have synced — a partial catalog that looks authoritative is
// worse than a 503.
func Run(ctx context.Context, opts Options, dir *directory.Directory) error {
	if opts.RestConfig == nil {
		return fmt.Errorf("rest config is required")
	}
	if opts.CatalogSlice == "" {
		opts.CatalogSlice = bootstrap.ExportCatalog
	}
	if opts.PublishSlice == "" {
		opts.PublishSlice = bootstrap.ExportPublish
	}

	logger := log.FromContext(ctx).WithName("marketplace-directory")

	sch := runtime.NewScheme()
	utilruntime.Must(scheme.AddToScheme(sch))
	utilruntime.Must(corev1alpha1.AddToScheme(sch))
	utilruntime.Must(apisv1alpha1.AddToScheme(sch))
	utilruntime.Must(apisv1alpha2.AddToScheme(sch))
	utilruntime.Must(marketplacev1alpha1.AddToScheme(sch))

	newMgr := func(slice string) (mcmanager.Manager, error) {
		provider, err := apiexport.New(opts.RestConfig, slice, apiexport.Options{
			Scheme:   sch,
			Log:      &logger,
			Handlers: handlers.Handlers{clusterLifecycle{dir: dir, logger: logger}},
		})
		if err != nil {
			return nil, fmt.Errorf("construct provider for %q: %w", slice, err)
		}
		return mcmanager.New(opts.RestConfig, provider, manager.Options{
			Scheme:  sch,
			Metrics: metricsserver.Options{BindAddress: "0"}, // the VW has its own HTTP server
		})
	}

	catalog, err := newMgr(opts.CatalogSlice)
	if err != nil {
		return err
	}
	publish, err := newMgr(opts.PublishSlice)
	if err != nil {
		return err
	}

	if err := mcbuilder.ControllerManagedBy(catalog).
		Named("marketplace-vw-producer").
		For(&marketplacev1alpha1.Producer{}).
		Complete(mcreconcile.Func(func(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
			key := directory.Key{Cluster: string(req.ClusterName), Name: req.Name}
			var p marketplacev1alpha1.Producer
			if err := getFrom(ctx, catalog, req, &p); err != nil {
				if apierrors.IsNotFound(err) {
					dir.RemoveProducer(key)
					return ctrl.Result{}, nil
				}
				return ctrl.Result{}, err
			}
			dir.UpsertProducer(key, directory.Producer{
				DisplayName:      p.Spec.DisplayName,
				Description:      p.Spec.Description,
				WorkspacePath:    p.Status.WorkspacePath,
				WorkspaceCluster: p.Status.WorkspaceCluster,
				Ready:            p.Status.Phase == marketplacev1alpha1.PhaseReady,
			})
			return ctrl.Result{}, nil
		})); err != nil {
		return fmt.Errorf("build producer mirror: %w", err)
	}

	// An offering is an APIExport that says it is one. Every export in
	// every producer workspace is mirrored or dropped by that annotation
	// alone, so un-publishing takes an entry off the catalog exactly as
	// deleting the export does.
	if err := mcbuilder.ControllerManagedBy(publish).
		Named("marketplace-vw-offering").
		For(&apisv1alpha2.APIExport{}).
		Complete(mcreconcile.Func(func(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
			key := directory.Key{Cluster: string(req.ClusterName), Name: req.Name}
			var export apisv1alpha2.APIExport
			if err := getFrom(ctx, publish, req, &export); err != nil {
				if apierrors.IsNotFound(err) {
					dir.RemoveOffering(key)
					return ctrl.Result{}, nil
				}
				return ctrl.Result{}, err
			}
			if !offering.Published(&export) {
				dir.RemoveOffering(key)
				return ctrl.Result{}, nil
			}
			card := offering.From(&export)
			dir.UpsertOffering(key, directory.Offering{
				DisplayName:     card.DisplayName,
				Description:     card.Description,
				Export:          export.Name,
				Revision:        card.Revision,
				Claims:          card.Claims,
				IdentityHash:    card.IdentityHash,
				DocsURL:         card.DocsURL,
				InstructionsRef: card.InstructionsRef,
				Resources:       resourceRefs(&export),
				// Ready means a consumer can act on this entry: kcp has
				// given the export its identity. Whether a Provider record
				// names this workspace is the other half, and the
				// directory decides that when it joins the two.
				Ready: card.IdentityHash != "",
			})
			return ctrl.Result{}, nil
		})); err != nil {
		return fmt.Errorf("build offering mirror: %w", err)
	}

	// Instructions: ConfigMaps in producer workspaces, mirrored whole. Every
	// ConfigMap the export's claim can see comes through here, and the
	// directory keeps all of them keyed by name — cheap, and it means a
	// producer editing their instructions needs no re-reconcile of the
	// export that points at them.
	if err := mcbuilder.ControllerManagedBy(publish).
		Named("marketplace-vw-instructions").
		For(&corev1.ConfigMap{}).
		Complete(mcreconcile.Func(func(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
			key := directory.Key{Cluster: string(req.ClusterName), Name: req.Name}
			var cm corev1.ConfigMap
			if err := getFrom(ctx, publish, req, &cm); err != nil {
				if apierrors.IsNotFound(err) {
					dir.RemoveInstructions(key)
					return ctrl.Result{}, nil
				}
				return ctrl.Result{}, err
			}
			dir.UpsertInstructions(key, cm.Data)
			return ctrl.Result{}, nil
		})); err != nil {
		return fmt.Errorf("build instructions mirror: %w", err)
	}

	// Schemas: the APIResourceSchemas exports reference, mirrored the same
	// way and for the same reason — joined by name at read time, so the
	// catalog shows what enabling an offering adds before anyone does.
	if err := mcbuilder.ControllerManagedBy(publish).
		Named("marketplace-vw-schemas").
		For(&apisv1alpha1.APIResourceSchema{}).
		Complete(mcreconcile.Func(func(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
			key := directory.Key{Cluster: string(req.ClusterName), Name: req.Name}
			var s apisv1alpha1.APIResourceSchema
			if err := getFrom(ctx, publish, req, &s); err != nil {
				if apierrors.IsNotFound(err) {
					dir.RemoveSchema(key)
					return ctrl.Result{}, nil
				}
				return ctrl.Result{}, err
			}
			dir.UpsertSchema(key, schemaOf(&s))
			return ctrl.Result{}, nil
		})); err != nil {
		return fmt.Errorf("build schema mirror: %w", err)
	}

	if err := catalog.GetLocalManager().Add(manager.RunnableFunc(func(ctx context.Context) error {
		if !catalog.GetLocalManager().GetCache().WaitForCacheSync(ctx) {
			return fmt.Errorf("catalog discovery cache did not sync")
		}
		if !publish.GetLocalManager().GetCache().WaitForCacheSync(ctx) {
			return fmt.Errorf("publish discovery cache did not sync")
		}
		dir.SetReady()
		logger.Info("marketplace directory ready")
		<-ctx.Done()
		return nil
	})); err != nil {
		return fmt.Errorf("register readiness runnable: %w", err)
	}

	errs := make(chan error, 2)
	go func() { errs <- catalog.Start(ctx) }()
	go func() { errs <- publish.Start(ctx) }()
	select {
	case err := <-errs:
		return err
	case <-ctx.Done():
		return nil
	}
}

type clusterLifecycle struct {
	dir    *directory.Directory
	logger logr.Logger
}

func (c clusterLifecycle) OnAdd(client.Object)                   {}
func (c clusterLifecycle) OnUpdate(client.Object, client.Object) {}

func (c clusterLifecycle) OnDelete(obj client.Object) {
	cluster := logicalcluster.From(obj).String()
	if cluster == "" {
		return
	}
	c.logger.Info("workspace left the fleet, dropping from directory", "cluster", cluster)
	c.dir.ForgetCluster(cluster)
}

func resourceRefs(export *apisv1alpha2.APIExport) []directory.ResourceRef {
	var refs []directory.ResourceRef
	for _, r := range export.Spec.Resources {
		refs = append(refs, directory.ResourceRef{Group: r.Group, Resource: r.Name, Schema: r.Schema})
	}
	return refs
}

func schemaOf(s *apisv1alpha1.APIResourceSchema) directory.Schema {
	out := directory.Schema{Kind: s.Spec.Names.Kind, Scope: string(s.Spec.Scope)}
	for _, v := range s.Spec.Versions {
		out.Versions = append(out.Versions, marketplacev1alpha1.OfferingResourceVersion{
			Name:    v.Name,
			Served:  v.Served,
			Storage: v.Storage,
			Schema:  *v.Schema.DeepCopy(),
		})
	}
	return out
}

func getFrom(ctx context.Context, mgr mcmanager.Manager, req mcreconcile.Request, obj client.Object) error {
	cl, err := mgr.GetCluster(ctx, req.ClusterName)
	if err != nil {
		return fmt.Errorf("get cluster %q: %w", req.ClusterName, err)
	}
	return cl.GetClient().Get(ctx, req.NamespacedName, obj)
}
