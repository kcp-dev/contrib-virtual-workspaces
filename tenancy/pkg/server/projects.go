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

package server

import (
	"context"
	"fmt"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kcp-dev/contrib-virtual-workspaces/tenancy/pkg/provider"
	"github.com/kcp-dev/contrib-virtual-workspaces/tenancy/pkg/virtual/membership"
	"github.com/kcp-dev/contrib-virtual-workspaces/tenancy/pkg/virtual/selfprojectrequest"
	"github.com/kcp-dev/contrib-virtual-workspaces/tenancy/pkg/virtual/selftenantrequest"
	tenancyv1alpha1 "github.com/kcp-dev/contrib-virtual-workspaces/tenancy/sdk/apis/tenancy/v1alpha1"
)

// projectWriter creates Projects in tenant workspaces through the tenancy
// APIExport's virtual workspace — the same export the directory reads, so
// the server needs no reach it does not already have.
//
// This is the credential a tenant admin deliberately does not hold. What
// keeps that honest is the narrowness of what it is used for: the storage
// authorizes the caller first, and then exactly one object is created
// from fields the caller controls. Nothing here can name a workspace,
// choose a cluster, or touch a Membership.
type projectWriter struct {
	clusters *provider.Clusters
}

func newProjectWriter(clusters *provider.Clusters) *projectWriter {
	return &projectWriter{clusters: clusters}
}

// CreateProject satisfies selfprojectrequest.CreateProjectFunc.
func (w *projectWriter) CreateProject(ctx context.Context, cluster string, project *tenancyv1alpha1.Project) (bool, error) {
	c, err := w.clusters.Tenancy(ctx, cluster)
	if err != nil {
		return false, err
	}

	if err := c.Create(ctx, project); err != nil {
		// Asking twice is not an error: the request says what should
		// exist, and it does.
		if apierrors.IsAlreadyExists(err) {
			return false, nil
		}
		return false, fmt.Errorf("create project %q in tenant workspace %s: %w", project.Name, cluster, err)
	}
	return true, nil
}

var _ selfprojectrequest.CreateProjectFunc = (*projectWriter)(nil).CreateProject

// membershipWriter changes grants inside tenant workspaces, through the
// same export the directory reads them from.
//
// This is the credential a tenant admin cannot hold: the grants governing
// their own access live beside the projects. What keeps it honest is that
// the storage authorizes the caller as an admin of that very tenant first,
// and derives both the cluster and the object name itself — nothing the
// caller sends chooses where a write lands.
type membershipWriter struct {
	clusters *provider.Clusters
}

func newMembershipWriter(clusters *provider.Clusters) *membershipWriter {
	return &membershipWriter{clusters: clusters}
}

// Create satisfies membership.CreateFunc. Re-granting the same access is
// an update in place rather than a conflict, because the request states
// what should be true and the name is derived from that.
func (w *membershipWriter) Create(ctx context.Context, cluster string, m *tenancyv1alpha1.Membership) (bool, error) {
	c, err := w.clusters.Tenancy(ctx, cluster)
	if err != nil {
		return false, err
	}

	if err := c.Create(ctx, m); err == nil {
		return true, nil
	} else if !apierrors.IsAlreadyExists(err) {
		return false, fmt.Errorf("grant %q in tenant workspace %s: %w", m.Name, cluster, err)
	}

	// Already there: bring the role (and anything else in the spec) to what
	// was asked for, retrying the read-modify-write on a conflicting update.
	spec := m.Spec
	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var existing tenancyv1alpha1.Membership
		if err := c.Get(ctx, client.ObjectKey{Name: m.Name}, &existing); err != nil {
			return err
		}
		if existing.Spec == spec {
			return nil
		}
		existing.Spec = spec
		return c.Update(ctx, &existing)
	})
	if err != nil {
		return false, fmt.Errorf("update grant %q in tenant workspace %s: %w", m.Name, cluster, err)
	}
	return false, nil
}

// Delete satisfies membership.DeleteFunc.
func (w *membershipWriter) Delete(ctx context.Context, cluster, name string) error {
	c, err := w.clusters.Tenancy(ctx, cluster)
	if err != nil {
		return err
	}
	m := &tenancyv1alpha1.Membership{ObjectMeta: metav1.ObjectMeta{Name: name}}
	if err := c.Delete(ctx, m); err != nil {
		// Gone is the state that was asked for.
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("revoke %q in tenant workspace %s: %w", name, cluster, err)
	}
	return nil
}

var (
	_ membership.CreateFunc = (*membershipWriter)(nil).Create
	_ membership.DeleteFunc = (*membershipWriter)(nil).Delete
)

// tenantWriter creates Tenants in the store workspace through the platform
// APIExport — the same export the directory reads Tenants from.
//
// Only one operation is offered through it, and the Tenant it writes names
// its first admin, so the credential cannot be used to grant anything to
// anyone the caller is not.
type tenantWriter struct {
	clusters *provider.Clusters
	store    string
}

func newTenantWriter(clusters *provider.Clusters, store string) *tenantWriter {
	return &tenantWriter{clusters: clusters, store: store}
}

// CreateTenant satisfies selftenantrequest.CreateTenantFunc.
func (w *tenantWriter) CreateTenant(ctx context.Context, tenant *tenancyv1alpha1.Tenant) (bool, error) {
	c, err := w.clusters.Platform(ctx, w.store)
	if err != nil {
		return false, err
	}

	if err := c.Create(ctx, tenant); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return false, nil
		}
		return false, fmt.Errorf("create tenant %q: %w", tenant.Name, err)
	}
	return true, nil
}

var _ selftenantrequest.CreateTenantFunc = (*tenantWriter)(nil).CreateTenant

// resolveCluster reads the logical cluster name of a workspace path.
//
// The writers address workspaces by cluster rather than by path, because
// an APIExport's virtual workspace serves clusters: it has no view of the
// tree the path walks.
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
