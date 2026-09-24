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

// Package provider populates the tenancy directory from the organization
// workspaces bound to the tenancy APIExport. It is the read side of the
// operator: the same discovery (multicluster-runtime over the apiexport
// endpoint slice), but instead of materializing workspaces it only mirrors
// Tenant, Project and Membership objects into the in-memory directory the
// virtual workspace answers from.
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

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"

	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/kcp-dev/multicluster-provider/apiexport"
	"github.com/kcp-dev/multicluster-provider/pkg/handlers"
	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	kcptenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"

	"github.com/kcp-dev/contrib-virtual-workspaces/tenancy/pkg/bootstrap"
	"github.com/kcp-dev/contrib-virtual-workspaces/tenancy/pkg/directory"
	tenancyv1alpha1 "github.com/kcp-dev/contrib-virtual-workspaces/tenancy/sdk/apis/tenancy/v1alpha1"
)

// Options configures the directory provider.
type Options struct {
	// RestConfig reaches the kcp shard hosting the tenancy APIExports.
	RestConfig *rest.Config
	// PlatformSlice serves Tenants, TenancySlice serves Projects and
	// Memberships. They are different exports because they live in
	// different tiers, so the directory is fed by two managers.
	PlatformSlice string
	TenancySlice  string
}

// Run fills dir until ctx is cancelled. It marks the directory ready once
// cluster discovery has synced, so reviews are never answered from a
// half-built index.
func Run(ctx context.Context, opts Options, dir *directory.Directory) error {
	if opts.RestConfig == nil {
		return fmt.Errorf("rest config is required")
	}
	if opts.PlatformSlice == "" {
		opts.PlatformSlice = bootstrap.ExportPlatform
	}
	if opts.TenancySlice == "" {
		opts.TenancySlice = bootstrap.ExportTenancy
	}

	logger := log.FromContext(ctx).WithName("tenancy-directory")

	sch := runtime.NewScheme()
	utilruntime.Must(scheme.AddToScheme(sch))
	utilruntime.Must(corev1alpha1.AddToScheme(sch))
	utilruntime.Must(kcptenancyv1alpha1.AddToScheme(sch))
	utilruntime.Must(apisv1alpha1.AddToScheme(sch))
	utilruntime.Must(tenancyv1alpha1.AddToScheme(sch))

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

	platform, err := newMgr(opts.PlatformSlice)
	if err != nil {
		return err
	}
	tenancy, err := newMgr(opts.TenancySlice)
	if err != nil {
		return err
	}

	if err := registerTenantMirror(platform, dir); err != nil {
		return err
	}
	if err := registerTenantScopedMirrors(tenancy, dir); err != nil {
		return err
	}

	// Readiness gates the review endpoint: answering before BOTH syncs
	// complete would return a partial answer that looks authoritative —
	// tenants without their memberships, or the reverse.
	if err := platform.GetLocalManager().Add(manager.RunnableFunc(func(ctx context.Context) error {
		if !platform.GetLocalManager().GetCache().WaitForCacheSync(ctx) {
			return fmt.Errorf("platform discovery cache did not sync")
		}
		if !tenancy.GetLocalManager().GetCache().WaitForCacheSync(ctx) {
			return fmt.Errorf("tenancy discovery cache did not sync")
		}
		dir.SetReady()
		logger.Info("tenancy directory ready")
		<-ctx.Done()
		return nil
	})); err != nil {
		return fmt.Errorf("register readiness runnable: %w", err)
	}

	errs := make(chan error, 2)
	go func() { errs <- platform.Start(ctx) }()
	go func() { errs <- tenancy.Start(ctx) }()
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
	c.logger.Info("organization workspace left the fleet, dropping from directory", "cluster", cluster)
	c.dir.ForgetCluster(cluster)
}

// registerTenantMirror mirrors Tenants from the platform tier.
func registerTenantMirror(mgr mcmanager.Manager, dir *directory.Directory) error {
	if err := mcbuilder.ControllerManagedBy(mgr).
		Named("tenancy-vw-tenant").
		For(&tenancyv1alpha1.Tenant{}).
		Complete(mcreconcile.Func(func(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
			key := directory.Key{Cluster: string(req.ClusterName), Name: req.Name}
			var tenant tenancyv1alpha1.Tenant
			if err := getFrom(ctx, mgr, req, &tenant); err != nil {
				if apierrors.IsNotFound(err) {
					dir.RemoveTenant(key)
					return ctrl.Result{}, nil
				}
				return ctrl.Result{}, err
			}
			dir.UpsertTenant(key, directory.Tenant{
				DisplayName:      tenant.Spec.DisplayName,
				WorkspaceCluster: tenant.Status.WorkspaceCluster,
			})
			return ctrl.Result{}, nil
		})); err != nil {
		return fmt.Errorf("build tenant mirror: %w", err)
	}
	return nil
}

// registerTenantScopedMirrors mirrors Projects and Memberships, which live
// inside each tenant's own workspace.
func registerTenantScopedMirrors(mgr mcmanager.Manager, dir *directory.Directory) error {
	if err := mcbuilder.ControllerManagedBy(mgr).
		Named("tenancy-vw-project").
		For(&tenancyv1alpha1.Project{}).
		Complete(mcreconcile.Func(func(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
			key := directory.Key{Cluster: string(req.ClusterName), Name: req.Name}
			var project tenancyv1alpha1.Project
			if err := getFrom(ctx, mgr, req, &project); err != nil {
				if apierrors.IsNotFound(err) {
					dir.RemoveProject(key)
					return ctrl.Result{}, nil
				}
				return ctrl.Result{}, err
			}
			dir.UpsertProject(key, directory.Project{
				Tenant:           project.Spec.Tenant,
				DisplayName:      project.Spec.DisplayName,
				WorkspaceCluster: project.Status.WorkspaceCluster,
			})
			return ctrl.Result{}, nil
		})); err != nil {
		return fmt.Errorf("build project mirror: %w", err)
	}

	if err := mcbuilder.ControllerManagedBy(mgr).
		Named("tenancy-vw-membership").
		For(&tenancyv1alpha1.Membership{}).
		Complete(mcreconcile.Func(func(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
			key := directory.Key{Cluster: string(req.ClusterName), Name: req.Name}
			var membership tenancyv1alpha1.Membership
			if err := getFrom(ctx, mgr, req, &membership); err != nil {
				if apierrors.IsNotFound(err) {
					dir.RemoveMembership(key)
					return ctrl.Result{}, nil
				}
				return ctrl.Result{}, err
			}
			dir.UpsertMembership(key, directory.Membership{
				Subject: membership.Spec.Subject,
				Role:    string(membership.Spec.Role),
				Tenant:  membership.Spec.Tenant,
				Project: membership.Spec.Project,
			})
			return ctrl.Result{}, nil
		})); err != nil {
		return fmt.Errorf("build membership mirror: %w", err)
	}

	return nil
}

func getFrom(ctx context.Context, mgr mcmanager.Manager, req mcreconcile.Request, obj client.Object) error {
	cl, err := mgr.GetCluster(ctx, req.ClusterName)
	if err != nil {
		return fmt.Errorf("get cluster %q: %w", req.ClusterName, err)
	}
	return cl.GetClient().Get(ctx, req.NamespacedName, obj)
}
