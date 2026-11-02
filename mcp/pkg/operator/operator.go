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

// Package operator turns a consumer's APIBinding into working MCP.
//
// Enabling MCP is not binding an API — MCP is an endpoint, not a resource.
// What a consumer is actually consenting to is letting the MCP server act
// as them in their own workspace, because behind the front-proxy their
// bearer token is consumed by the proxy and the server has nothing to
// replay. It therefore impersonates them, and kcp checks that right in
// every workspace it reaches into.
//
// So the binding carries permission claims on ClusterRoles and
// ClusterRoleBindings, and this operator spends them on exactly one thing:
// writing the impersonation grant into the workspace that just consented.
// It then records an MCPEndpoint there, so the consumer can see that it
// worked and where to point a client.
//
// There is one workspace this operator can never reach: one that has not
// bound the export. A cluster appears on the endpoint slice because it
// bound, and the claims only apply where the binding is — so consent and
// capability are the same fact.
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
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/retry"

	"github.com/kcp-dev/multicluster-provider/apiexport"
	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"

	"github.com/kcp-dev/contrib-virtual-workspaces/mcp/pkg/bootstrap"
	mcpv1alpha1 "github.com/kcp-dev/contrib-virtual-workspaces/mcp/sdk/apis/mcp/v1alpha1"
)

// endpointName is the single MCPEndpoint this operator maintains per
// workspace. One name, because there is one MCP server and one answer to
// "where do I point my client".
const endpointName = "mcp"

// impersonatorRole is the grant this operator writes, and the only thing
// it ever writes.
const impersonatorRole = "mcp-virtual-workspace-impersonator"

// Options configures the operator.
type Options struct {
	// RestConfig reaches the kcp shard hosting the mcp APIExport.
	RestConfig *rest.Config
	// Slice is the endpoint slice following the mcp export; the clusters
	// on it are the workspaces that enabled MCP.
	Slice string
	// ServerURL is where an MCP client connects, reported to consumers.
	ServerURL string
	// ImpersonateUser is the identity the MCP server presents, and so the
	// subject the impersonation grant names.
	ImpersonateUser string
	// Toolsets is reported so a consumer can see what the server exposes.
	Toolsets []string
}

// Run starts the operator and blocks until ctx is cancelled.
func Run(ctx context.Context, opts Options) error {
	if opts.RestConfig == nil {
		return fmt.Errorf("rest config is required")
	}
	if opts.ImpersonateUser == "" {
		return fmt.Errorf("--impersonate-user is required: it is the subject the grant names")
	}
	if opts.Slice == "" {
		opts.Slice = bootstrap.ExportName
	}

	logger := log.FromContext(ctx).WithName("mcp-operator")
	sch := buildScheme()

	provider, err := apiexport.New(opts.RestConfig, opts.Slice, apiexport.Options{Scheme: sch, Log: &logger})
	if err != nil {
		return fmt.Errorf("construct provider for %q: %w", opts.Slice, err)
	}
	mgr, err := mcmanager.New(opts.RestConfig, provider, manager.Options{
		Scheme:  sch,
		Metrics: metricsserver.Options{BindAddress: "0"},
	})
	if err != nil {
		return fmt.Errorf("construct manager: %w", err)
	}

	r := &reconciler{mgr: mgr, opts: opts, logger: logger}

	// Reconciling the LogicalCluster is how a per-workspace action is
	// expressed: every bound workspace has exactly one, so one event per
	// workspace is exactly what is wanted. There is no consumer object to
	// watch — the binding is the request, and it lives on their side.
	if err := mcbuilder.ControllerManagedBy(mgr).
		Named("mcp-enablement").
		For(&corev1alpha1.LogicalCluster{}).
		Complete(mcreconcile.Func(r.reconcile)); err != nil {
		return fmt.Errorf("build enablement controller: %w", err)
	}

	logger.Info("mcp operator running",
		"slice", opts.Slice, "serverURL", opts.ServerURL,
		"impersonateUser", opts.ImpersonateUser, "toolsets", opts.Toolsets)
	return mgr.Start(ctx)
}

func buildScheme() *runtime.Scheme {
	sch := runtime.NewScheme()
	utilruntime.Must(scheme.AddToScheme(sch))
	utilruntime.Must(corev1alpha1.AddToScheme(sch))
	utilruntime.Must(apisv1alpha1.AddToScheme(sch))
	utilruntime.Must(apisv1alpha2.AddToScheme(sch))
	utilruntime.Must(mcpv1alpha1.AddToScheme(sch))
	return sch
}

type reconciler struct {
	mgr    mcmanager.Manager
	opts   Options
	logger logr.Logger
}

func (r *reconciler) reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("cluster", req.ClusterName)

	cl, err := r.mgr.GetCluster(ctx, req.ClusterName)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("get cluster %q: %w", req.ClusterName, err)
	}
	c := cl.GetClient()

	var lc corev1alpha1.LogicalCluster
	if err := c.Get(ctx, types.NamespacedName{Name: "cluster"}, &lc); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	status := mcpv1alpha1.MCPEndpointStatus{
		Phase:    mcpv1alpha1.PhaseReady,
		URL:      r.opts.ServerURL,
		Toolsets: r.opts.Toolsets,
	}
	if err := r.ensureImpersonation(ctx, c); err != nil {
		// Reported rather than only logged: the consumer enabled this and
		// is owed an answer in their own workspace.
		status.Phase = mcpv1alpha1.PhaseError
		status.Message = "granting impersonation: " + err.Error()
		status.URL = ""
		if err := r.report(ctx, c, status); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	if err := r.report(ctx, c, status); err != nil {
		return ctrl.Result{}, err
	}

	logger.V(2).Info("mcp enabled in workspace",
		"path", lc.Annotations["kcp.io/path"], "url", status.URL)
	return ctrl.Result{}, nil
}

// ensureImpersonation writes the grant, and nothing but the grant.
//
// Impersonation is the whole of what the MCP server needs and deliberately
// all it gets: every resource it touches, it touches as the caller, so kcp
// authorizes each request against their rights and the audit log records
// both identities. Giving this identity resource permissions of its own
// would quietly turn that into something else.
func (r *reconciler) ensureImpersonation(ctx context.Context, c client.Client) error {
	role := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{Name: impersonatorRole},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{""},
				Resources: []string{"users", "groups", "serviceaccounts"},
				Verbs:     []string{"impersonate"},
			},
			{
				// Extras are authorized as userextras/<key> and UIDs as
				// uids, both in authentication.k8s.io. RBAC has no
				// subresource wildcard, so the group is granted whole.
				APIGroups: []string{"authentication.k8s.io"},
				Resources: []string{"*"},
				Verbs:     []string{"impersonate"},
			},
			{
				Verbs:           []string{"access"},
				NonResourceURLs: []string{"/"},
			},
		},
	}
	binding := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: impersonatorRole},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: impersonatorRole},
		Subjects: []rbacv1.Subject{{
			APIGroup: rbacv1.GroupName, Kind: rbacv1.UserKind, Name: r.opts.ImpersonateUser,
		}},
	}

	if err := upsert(ctx, c, role, func(existing client.Object) {
		existing.(*rbacv1.ClusterRole).Rules = role.Rules
	}); err != nil {
		return err
	}
	return upsert(ctx, c, binding, func(existing client.Object) {
		b := existing.(*rbacv1.ClusterRoleBinding)
		b.Subjects, b.RoleRef = binding.Subjects, binding.RoleRef
	})
}

// report records what a consumer needs to know, in their own workspace.
func (r *reconciler) report(ctx context.Context, c client.Client, status mcpv1alpha1.MCPEndpointStatus) error {
	endpoint := &mcpv1alpha1.MCPEndpoint{ObjectMeta: metav1.ObjectMeta{Name: endpointName}}
	if err := c.Create(ctx, endpoint); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create MCPEndpoint: %w", err)
	}
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var existing mcpv1alpha1.MCPEndpoint
		if err := c.Get(ctx, types.NamespacedName{Name: endpointName}, &existing); err != nil {
			return err
		}
		if equalStatus(existing.Status, status) {
			return nil
		}
		existing.Status = status
		return c.Status().Update(ctx, &existing)
	})
}

func equalStatus(a, b mcpv1alpha1.MCPEndpointStatus) bool {
	if a.Phase != b.Phase || a.Message != b.Message || a.URL != b.URL || len(a.Toolsets) != len(b.Toolsets) {
		return false
	}
	for i := range a.Toolsets {
		if a.Toolsets[i] != b.Toolsets[i] {
			return false
		}
	}
	return true
}

// upsert creates an object, or brings an existing one to what was asked
// for, retrying the read-modify-write on a conflicting update.
func upsert(ctx context.Context, c client.Client, desired client.Object, apply func(client.Object)) error {
	if err := c.Create(ctx, desired.DeepCopyObject().(client.Object)); err == nil {
		return nil
	} else if !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create %T %s: %w", desired, desired.GetName(), err)
	}
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		existing := desired.DeepCopyObject().(client.Object)
		if err := c.Get(ctx, types.NamespacedName{Name: desired.GetName()}, existing); err != nil {
			return err
		}
		apply(existing)
		return c.Update(ctx, existing)
	})
}
