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

// Package membership serves the grants of the tenants a caller
// administers, so RBAC can be managed without entering the workspace the
// grants live in.
//
// Memberships live inside their tenant's workspace, which is closed to the
// tenant's own members — including its admins — precisely because it holds
// the records that decide their access. That makes it the one part of the
// model a tenant admin cannot administer directly, which is not a gap to
// leave open: someone has to be able to add a colleague.
//
// So this is mediated the same way project creation is. The caller's own
// admin Membership, as the directory already reports it, is the
// authorization; the server's credential performs the write; and what is
// offered through it is only ever a Membership in a tenant the caller
// already administers. A caller cannot name a workspace or a cluster, and
// cannot reach a tenant they do not administer — not even to discover that
// it exists.
//
// Listing here is deliberately wider than a self-review: it reports what
// OTHER people hold, which is the point of administering access, and is
// why every path checks for admin rather than for mere membership.
package membership

import (
	"context"
	"fmt"
	"slices"
	"sort"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metainternalversion "k8s.io/apimachinery/pkg/apis/meta/internalversion"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/apiserver/pkg/authentication/user"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/apiserver/pkg/registry/rest"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/contrib-virtual-workspaces/tenancy/pkg/directory"
	"github.com/kcp-dev/contrib-virtual-workspaces/tenancy/pkg/naming"
	tenancyv1alpha1 "github.com/kcp-dev/contrib-virtual-workspaces/tenancy/sdk/apis/tenancy/v1alpha1"
)

// CreateFunc upserts a Membership in one tenant's workspace. The server
// supplies it, because how the virtual workspace reaches that workspace is
// a detail of the deployment rather than of the API.
type CreateFunc func(ctx context.Context, cluster string, m *tenancyv1alpha1.Membership) (created bool, err error)

// DeleteFunc removes a Membership from one tenant's workspace.
type DeleteFunc func(ctx context.Context, cluster, name string) error

// Options carries the write side. Both funcs may be nil, in which case the
// resource stays readable and says plainly that changing grants is not
// configured rather than appearing to work.
type Options struct {
	Create CreateFunc
	Delete DeleteFunc
}

// REST serves memberships for the tenants the caller administers.
type REST struct {
	directory *directory.Directory
	opts      Options
}

var (
	_ rest.Storage              = &REST{}
	_ rest.Lister               = &REST{}
	_ rest.Getter               = &REST{}
	_ rest.Creater              = &REST{}
	_ rest.GracefulDeleter      = &REST{}
	_ rest.Scoper               = &REST{}
	_ rest.SingularNameProvider = &REST{}
)

// NewREST returns the storage.
func NewREST(d *directory.Directory, opts Options) *REST {
	return &REST{directory: d, opts: opts}
}

func (r *REST) New() runtime.Object { return &tenancyv1alpha1.Membership{} }

func (r *REST) NewList() runtime.Object { return &tenancyv1alpha1.MembershipList{} }

func (r *REST) Destroy() {}

func (r *REST) NamespaceScoped() bool { return false }

func (r *REST) GetSingularName() string { return "membership" }

func (r *REST) ConvertToTable(ctx context.Context, object runtime.Object, tableOptions runtime.Object) (*metav1.Table, error) {
	return rest.NewDefaultTableConvertor(tenancyv1alpha1.Resource("memberships")).
		ConvertToTable(ctx, object, tableOptions)
}

// List returns every grant in every tenant the caller administers.
//
// There is no scoping parameter: the answer is the caller's whole
// administrative surface, and a client that cares about one tenant filters
// on spec.tenant. That keeps the authorization rule here a single
// sentence, which is worth more than saving the client a loop.
func (r *REST) List(ctx context.Context, _ *metainternalversion.ListOptions) (runtime.Object, error) {
	admin, err := r.adminTenants(ctx)
	if err != nil {
		return nil, err
	}

	list := &tenancyv1alpha1.MembershipList{}
	for _, claim := range admin {
		for _, m := range r.directory.MembershipsIn(claim.Cluster) {
			list.Items = append(list.Items, membershipObject(m))
		}
	}
	// Stable across calls: by tenant, then by name.
	sort.Slice(list.Items, func(i, j int) bool {
		if list.Items[i].Spec.Tenant != list.Items[j].Spec.Tenant {
			return list.Items[i].Spec.Tenant < list.Items[j].Spec.Tenant
		}
		return list.Items[i].Name < list.Items[j].Name
	})
	return list, nil
}

// Get returns one grant, if it is in a tenant the caller administers.
func (r *REST) Get(ctx context.Context, name string, _ *metav1.GetOptions) (runtime.Object, error) {
	admin, err := r.adminTenants(ctx)
	if err != nil {
		return nil, err
	}
	for _, claim := range admin {
		for _, m := range r.directory.MembershipsIn(claim.Cluster) {
			if m.Name == name {
				out := membershipObject(m)
				return &out, nil
			}
		}
	}
	return nil, apierrors.NewNotFound(tenancyv1alpha1.Resource("memberships"), name)
}

// Create grants access in a tenant the caller administers.
//
// The object's name is derived from what is being granted to whom, not
// taken from the request: two requests saying the same thing are the same
// grant, so re-granting changes the role in place instead of leaving two
// objects disagreeing about it.
func (r *REST) Create(ctx context.Context, obj runtime.Object, _ rest.ValidateObjectFunc, _ *metav1.CreateOptions) (runtime.Object, error) {
	m, ok := obj.(*tenancyv1alpha1.Membership)
	if !ok {
		return nil, apierrors.NewBadRequest(fmt.Sprintf("not a Membership: %T", obj))
	}
	if r.opts.Create == nil {
		return nil, apierrors.NewServiceUnavailable(
			"this virtual workspace was started without membership management; grants are read-only here")
	}

	if errs := validateSpec(&m.Spec); len(errs) > 0 {
		return nil, apierrors.NewInvalid(
			tenancyv1alpha1.SchemeGroupVersion.WithKind("Membership").GroupKind(), m.Name, errs)
	}

	claim, err := r.adminOn(ctx, m.Spec.Tenant)
	if err != nil {
		return nil, err
	}
	// A grant naming a project that does not exist would be a grant that
	// never applies to anything — worth refusing while the caller is still
	// looking at it.
	if m.Spec.Project != "" && !slices.ContainsFunc(claim.Projects,
		func(p tenancyv1alpha1.ProjectClaim) bool { return p.Name == m.Spec.Project }) {
		return nil, apierrors.NewInvalid(
			tenancyv1alpha1.SchemeGroupVersion.WithKind("Membership").GroupKind(), m.Name,
			field.ErrorList{field.NotFound(
				field.NewPath("spec", "project"), m.Spec.Project)})
	}

	grant := &tenancyv1alpha1.Membership{
		ObjectMeta: metav1.ObjectMeta{
			Name: naming.MembershipName(m.Spec.Subject.Name, m.Spec.Tenant, m.Spec.Project),
		},
		Spec: m.Spec,
	}

	created, err := r.opts.Create(ctx, claim.Cluster, grant)
	if err != nil {
		return nil, err
	}

	user, _ := genericapirequest.UserFrom(ctx)
	klog.FromContext(ctx).V(2).Info("granted membership",
		"by", userName(user), "subject", m.Spec.Subject.Name, "role", m.Spec.Role,
		"tenant", m.Spec.Tenant, "project", m.Spec.Project,
		"cluster", claim.Cluster, "created", created)

	return grant, nil
}

// Delete revokes a grant in a tenant the caller administers.
func (r *REST) Delete(ctx context.Context, name string, _ rest.ValidateObjectFunc, _ *metav1.DeleteOptions) (runtime.Object, bool, error) {
	if r.opts.Delete == nil {
		return nil, false, apierrors.NewServiceUnavailable(
			"this virtual workspace was started without membership management; grants are read-only here")
	}

	admin, err := r.adminTenants(ctx)
	if err != nil {
		return nil, false, err
	}

	for _, claim := range admin {
		grants := r.directory.MembershipsIn(claim.Cluster)
		idx := slices.IndexFunc(grants, func(m directory.NamedMembership) bool { return m.Name == name })
		if idx < 0 {
			continue
		}
		found := grants[idx]

		// Refuse the one deletion that cannot be undone through this API:
		// removing the last tenant-wide admin leaves nobody who may grant
		// anything, including the grant that would fix it.
		if found.Role == string(tenancyv1alpha1.RoleAdmin) && found.Project == "" &&
			lastTenantAdmin(grants, found.Tenant) {
			return nil, false, apierrors.NewForbidden(
				tenancyv1alpha1.Resource("memberships"), name,
				fmt.Errorf("this is the last admin of tenant %q; grant admin to someone else first, "+
					"or nobody will be able to", found.Tenant))
		}

		if err := r.opts.Delete(ctx, claim.Cluster, name); err != nil {
			return nil, false, err
		}

		user, _ := genericapirequest.UserFrom(ctx)
		klog.FromContext(ctx).V(2).Info("revoked membership",
			"by", userName(user), "name", name, "tenant", found.Tenant, "cluster", claim.Cluster)

		out := membershipObject(found)
		return &out, true, nil
	}

	return nil, false, apierrors.NewNotFound(tenancyv1alpha1.Resource("memberships"), name)
}

// adminTenants returns the caller's claims on the tenants they administer
// and whose workspace exists.
func (r *REST) adminTenants(ctx context.Context) ([]tenancyv1alpha1.TenantClaim, error) {
	if !r.directory.Ready() {
		return nil, apierrors.NewServiceUnavailable("tenancy directory is not ready; try again shortly")
	}
	user, ok := genericapirequest.UserFrom(ctx)
	if !ok {
		return nil, apierrors.NewUnauthorized("no user present in request context")
	}

	// noEndpoints: this path needs roles and clusters, not URLs.
	var admin []tenancyv1alpha1.TenantClaim
	for _, claim := range r.directory.ReviewFor(user.GetName(), user.GetGroups(), noEndpoints) {
		if slices.Contains(claim.Roles, string(tenancyv1alpha1.RoleAdmin)) && claim.Cluster != "" {
			admin = append(admin, claim)
		}
	}
	return admin, nil
}

// adminOn resolves one tenant the caller administers, or explains the
// refusal without revealing whether the tenant exists.
func (r *REST) adminOn(ctx context.Context, tenant string) (tenancyv1alpha1.TenantClaim, error) {
	admin, err := r.adminTenants(ctx)
	if err != nil {
		return tenancyv1alpha1.TenantClaim{}, err
	}
	for _, claim := range admin {
		if claim.Name == tenant {
			return claim, nil
		}
	}
	return tenancyv1alpha1.TenantClaim{}, apierrors.NewForbidden(
		tenancyv1alpha1.Resource("memberships"), "",
		fmt.Errorf("you are not an admin of tenant %q", tenant))
}

// lastTenantAdmin reports whether exactly one tenant-wide admin grant
// covers the tenant.
func lastTenantAdmin(grants []directory.NamedMembership, tenant string) bool {
	count := 0
	for _, g := range grants {
		if g.Tenant == tenant && g.Project == "" && g.Role == string(tenancyv1alpha1.RoleAdmin) {
			count++
		}
	}
	return count <= 1
}

// membershipObject renders the directory's projection back as the API
// object it was mirrored from.
func membershipObject(m directory.NamedMembership) tenancyv1alpha1.Membership {
	return tenancyv1alpha1.Membership{
		ObjectMeta: metav1.ObjectMeta{Name: m.Name},
		Spec: tenancyv1alpha1.MembershipSpec{
			Subject: m.Subject,
			Role:    tenancyv1alpha1.Role(m.Role),
			Tenant:  m.Tenant,
			Project: m.Project,
		},
	}
}

func userName(u user.Info) string {
	if u == nil {
		return ""
	}
	return u.GetName()
}

// noEndpoints renders no URL, for the paths that do not need one.
func noEndpoints(string) string { return "" }

// validateSpec checks what the caller controls.
func validateSpec(spec *tenancyv1alpha1.MembershipSpec) field.ErrorList {
	var errs field.ErrorList
	path := field.NewPath("spec")

	switch spec.Subject.Kind {
	case tenancyv1alpha1.SubjectKindUser, tenancyv1alpha1.SubjectKindGroup:
	default:
		errs = append(errs, field.NotSupported(path.Child("subject", "kind"), spec.Subject.Kind,
			[]string{string(tenancyv1alpha1.SubjectKindUser), string(tenancyv1alpha1.SubjectKindGroup)}))
	}
	if spec.Subject.Name == "" {
		errs = append(errs, field.Required(path.Child("subject", "name"), "who the grant is for"))
	} else if naming.Slugify(spec.Subject.Name) == "" {
		// The subject becomes part of an object name, so something of it has
		// to survive slugification.
		errs = append(errs, field.Invalid(path.Child("subject", "name"), spec.Subject.Name,
			"must contain at least one letter or digit"))
	}
	switch spec.Role {
	case tenancyv1alpha1.RoleView, tenancyv1alpha1.RoleEdit, tenancyv1alpha1.RoleAdmin:
	default:
		errs = append(errs, field.NotSupported(path.Child("role"), spec.Role,
			[]string{string(tenancyv1alpha1.RoleView), string(tenancyv1alpha1.RoleEdit), string(tenancyv1alpha1.RoleAdmin)}))
	}
	if spec.Tenant == "" {
		errs = append(errs, field.Required(path.Child("tenant"), "which tenant the grant applies to"))
	}
	return errs
}
