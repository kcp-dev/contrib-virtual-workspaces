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

// Package selfprojectrequest implements the one write the tenancy virtual
// workspace performs on a caller's behalf: creating a project in a tenant
// the caller administers.
//
// Everything else this component serves is a read of the caller's own
// state. This is a write, so it is worth being explicit about why it is
// mediated rather than delegated:
//
// Projects live in their tenant's workspace, which is also where the
// Memberships that decide who may reach what live. Granting a tenant
// admin RBAC there to create projects would hand them the records
// governing their own access. The virtual workspace therefore holds the
// only credential into that tier, and offers exactly one operation
// through it — create a project — gated on the caller's own admin
// Membership as the directory already reports it.
//
// The mediation is narrow on purpose: one verb, one kind, one field the
// caller controls. A caller cannot name a workspace, choose a cluster, or
// touch anything else in the tenant tier.
package selfprojectrequest

import (
	"context"
	"fmt"
	"slices"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/validation"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/apiserver/pkg/registry/rest"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/contrib-virtual-workspaces/tenancy/pkg/directory"
	tenancyv1alpha1 "github.com/kcp-dev/contrib-virtual-workspaces/tenancy/sdk/apis/tenancy/v1alpha1"
)

// CreateProjectFunc creates a Project in one tenant's workspace. The
// server supplies it, because how the virtual workspace reaches that
// workspace — through the tenancy export — is a detail of the deployment
// rather than of the API.
type CreateProjectFunc func(ctx context.Context, cluster string, project *tenancyv1alpha1.Project) (created bool, err error)

// REST answers SelfProjectRequest creates.
type REST struct {
	directory *directory.Directory
	create    CreateProjectFunc
}

var (
	_ rest.Storage              = &REST{}
	_ rest.Creater              = &REST{}
	_ rest.Scoper               = &REST{}
	_ rest.SingularNameProvider = &REST{}
)

// NewREST returns the storage. create may be nil, in which case the
// resource reports that project creation is not configured rather than
// pretending to work.
func NewREST(d *directory.Directory, create CreateProjectFunc) *REST {
	return &REST{directory: d, create: create}
}

func (r *REST) New() runtime.Object { return &tenancyv1alpha1.SelfProjectRequest{} }

func (r *REST) Destroy() {}

func (r *REST) NamespaceScoped() bool { return false }

func (r *REST) GetSingularName() string { return "selfprojectrequest" }

// Create authorizes the request against the caller's own membership and,
// if it holds, creates the project.
func (r *REST) Create(ctx context.Context, obj runtime.Object, _ rest.ValidateObjectFunc, _ *metav1.CreateOptions) (runtime.Object, error) {
	req, ok := obj.(*tenancyv1alpha1.SelfProjectRequest)
	if !ok {
		return nil, apierrors.NewBadRequest(fmt.Sprintf("not a SelfProjectRequest: %T", obj))
	}
	if r.create == nil {
		return nil, apierrors.NewServiceUnavailable(
			"this virtual workspace was started without project creation; it can only answer reviews")
	}
	if !r.directory.Ready() {
		return nil, apierrors.NewServiceUnavailable("tenancy directory is not ready; try again shortly")
	}

	user, ok := genericapirequest.UserFrom(ctx)
	if !ok {
		return nil, apierrors.NewUnauthorized("no user present in request context")
	}

	if errs := validateSpec(&req.Spec); len(errs) > 0 {
		return nil, apierrors.NewInvalid(
			tenancyv1alpha1.SchemeGroupVersion.WithKind("SelfProjectRequest").GroupKind(),
			req.Name, errs)
	}

	// The caller's own answer decides: admin on that tenant, or nothing.
	// Deliberately the same view `whoami` returns, so what a person is
	// told they can do and what they can do cannot drift apart.
	// noEndpoints: this path needs the caller's roles and the tenant's
	// cluster, not URLs, and ReviewFor calls the renderer for every
	// workspace it reports.
	claim, ok := tenantFor(
		r.directory.ReviewFor(user.GetName(), user.GetGroups(), noEndpoints),
		req.Spec.Tenant,
	)
	if !ok {
		// Not revealing whether the tenant exists: to a non-member the two
		// cases are the same, and saying which would leak the tenant list.
		return nil, apierrors.NewForbidden(
			tenancyv1alpha1.Resource("selfprojectrequests"), req.Spec.Name,
			fmt.Errorf("you are not an admin of tenant %q", req.Spec.Tenant))
	}
	if !slices.Contains(claim.Roles, string(tenancyv1alpha1.RoleAdmin)) {
		return nil, apierrors.NewForbidden(
			tenancyv1alpha1.Resource("selfprojectrequests"), req.Spec.Name,
			fmt.Errorf("creating a project in tenant %q needs the admin role; you hold %v",
				req.Spec.Tenant, claim.Roles))
	}
	if claim.Cluster == "" {
		return nil, apierrors.NewServiceUnavailable(
			fmt.Sprintf("tenant %q has no workspace yet; try again once it is ready", req.Spec.Tenant))
	}

	displayName := req.Spec.DisplayName
	if displayName == "" {
		displayName = req.Spec.Name
	}

	project := &tenancyv1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: req.Spec.Name},
		Spec: tenancyv1alpha1.ProjectSpec{
			Tenant:      req.Spec.Tenant,
			DisplayName: displayName,
			Description: req.Spec.Description,
		},
	}

	created, err := r.create(ctx, claim.Cluster, project)
	if err != nil {
		return nil, err
	}

	logger := klog.FromContext(ctx)
	logger.V(2).Info("answered SelfProjectRequest",
		"username", user.GetName(),
		"tenant", req.Spec.Tenant,
		"project", req.Spec.Name,
		"cluster", claim.Cluster,
		"created", created,
	)

	out := req.DeepCopy()
	out.Status = tenancyv1alpha1.SelfProjectRequestStatus{
		Created: created,
		Project: req.Spec.Name,
		Message: message(created, req.Spec.Name, req.Spec.Tenant),
	}
	return out, nil
}

func message(created bool, project, tenant string) string {
	if created {
		return fmt.Sprintf("project %q created in tenant %q", project, tenant)
	}
	return fmt.Sprintf("project %q already exists in tenant %q", project, tenant)
}

// noEndpoints renders no URL, for the paths that do not need one.
func noEndpoints(string) string { return "" }

// tenantFor finds the caller's claim on one tenant.
func tenantFor(claims []tenancyv1alpha1.TenantClaim, name string) (tenancyv1alpha1.TenantClaim, bool) {
	for _, c := range claims {
		if c.Name == name {
			return c, true
		}
	}
	return tenancyv1alpha1.TenantClaim{}, false
}

// validateSpec checks what the caller controls. The name becomes an
// object name and seeds a workspace name, so it has to be a DNS label
// rather than whatever was typed.
func validateSpec(spec *tenancyv1alpha1.SelfProjectRequestSpec) field.ErrorList {
	var errs field.ErrorList
	path := field.NewPath("spec")

	if spec.Tenant == "" {
		errs = append(errs, field.Required(path.Child("tenant"), "which tenant to create the project in"))
	}
	if spec.Name == "" {
		errs = append(errs, field.Required(path.Child("name"), "the project's name"))
	} else {
		for _, msg := range validation.NameIsDNSLabel(spec.Name, false) {
			errs = append(errs, field.Invalid(path.Child("name"), spec.Name, msg))
		}
	}
	return errs
}
