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

// Package selftenantrequest lets a caller with nothing get their first
// tenant.
//
// It closes the one gap that made the model unusable for a new person:
// every other self-service path here is authorized against a Membership,
// and someone who has just signed in has none. There is no login hook to
// hang this on either — the OIDC redirect goes from the browser to the
// identity provider and back, and this server only ever sees the
// resulting token on ordinary requests — so a new user has to ask for a
// tenant explicitly. That is deliberate rather than a limitation:
// provisioning as a side effect of a read would make listing tenants a
// write, and a monitoring probe would materialize workspaces.
//
// Because there is no Membership to check, the gate is configuration:
// self-service is off unless the deployment turns it on, and can be
// restricted to groups the identity provider asserts. A deployment that
// leaves it off is not broken — it has decided that handing out tenants
// is an administrator's job.
//
// The caller names the tenant and nothing else. The first admin is the
// caller, set here rather than read from the request, so nobody can ask
// for a tenant administered by someone else.
package selftenantrequest

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

	tenancyv1alpha1 "github.com/kcp-dev/contrib-virtual-workspaces/tenancy/sdk/apis/tenancy/v1alpha1"
)

// CreateTenantFunc creates a Tenant in the store workspace. The server
// supplies it, because where the records live is a deployment detail.
type CreateTenantFunc func(ctx context.Context, tenant *tenancyv1alpha1.Tenant) (created bool, err error)

// Options configures who may ask.
type Options struct {
	// Enabled turns self-service on. Off means the resource answers that
	// it is not offered here, which is a decision rather than a failure.
	Enabled bool
	// Groups, when set, restricts self-service to callers in at least one
	// of them — the groups the identity provider asserts, as kcp sees
	// them. Empty means any authenticated caller.
	Groups []string
}

// REST answers SelfTenantRequest creates.
type REST struct {
	opts   Options
	create CreateTenantFunc
}

var (
	_ rest.Storage              = &REST{}
	_ rest.Creater              = &REST{}
	_ rest.Scoper               = &REST{}
	_ rest.SingularNameProvider = &REST{}
)

func NewREST(opts Options, create CreateTenantFunc) *REST {
	return &REST{opts: opts, create: create}
}

func (r *REST) New() runtime.Object { return &tenancyv1alpha1.SelfTenantRequest{} }

func (r *REST) Destroy() {}

func (r *REST) NamespaceScoped() bool { return false }

func (r *REST) GetSingularName() string { return "selftenantrequest" }

// Create makes the tenant, with the caller as its first admin.
func (r *REST) Create(ctx context.Context, obj runtime.Object, _ rest.ValidateObjectFunc, _ *metav1.CreateOptions) (runtime.Object, error) {
	req, ok := obj.(*tenancyv1alpha1.SelfTenantRequest)
	if !ok {
		return nil, apierrors.NewBadRequest(fmt.Sprintf("not a SelfTenantRequest: %T", obj))
	}
	if r.create == nil || !r.opts.Enabled {
		return nil, apierrors.NewServiceUnavailable(
			"self-service tenants are not enabled here; an administrator creates tenants")
	}

	user, ok := genericapirequest.UserFrom(ctx)
	if !ok {
		return nil, apierrors.NewUnauthorized("no user present in request context")
	}

	if len(r.opts.Groups) > 0 && !slices.ContainsFunc(user.GetGroups(), func(g string) bool {
		return slices.Contains(r.opts.Groups, g)
	}) {
		return nil, apierrors.NewForbidden(
			tenancyv1alpha1.Resource("selftenantrequests"), req.Spec.Name,
			fmt.Errorf("self-service tenants are limited to %v", r.opts.Groups))
	}

	if errs := validateSpec(&req.Spec); len(errs) > 0 {
		return nil, apierrors.NewInvalid(
			tenancyv1alpha1.SchemeGroupVersion.WithKind("SelfTenantRequest").GroupKind(),
			req.Spec.Name, errs)
	}

	displayName := req.Spec.DisplayName
	if displayName == "" {
		displayName = req.Spec.Name
	}

	tenant := &tenancyv1alpha1.Tenant{
		ObjectMeta: metav1.ObjectMeta{Name: req.Spec.Name},
		Spec: tenancyv1alpha1.TenantSpec{
			DisplayName: displayName,
			Description: req.Spec.Description,
			// The caller, never what the request asked for: a tenant you
			// requested is a tenant you administer, and not one you can
			// hand to somebody else on the way in.
			FirstAdmin: user.GetName(),
		},
	}

	created, err := r.create(ctx, tenant)
	if err != nil {
		return nil, err
	}

	klog.FromContext(ctx).V(2).Info("answered SelfTenantRequest",
		"username", user.GetName(), "tenant", req.Spec.Name, "created", created)

	out := req.DeepCopy()
	out.Status = tenancyv1alpha1.SelfTenantRequestStatus{
		Created: created,
		Tenant:  req.Spec.Name,
		Message: message(created, req.Spec.Name, user.GetName()),
	}
	return out, nil
}

func message(created bool, tenant, admin string) string {
	if created {
		return fmt.Sprintf("tenant %q created; %s is its admin once the workspace is ready", tenant, admin)
	}
	// Someone else may hold it. Saying so is better than implying it is
	// theirs, and the review will show whether they can reach it.
	return fmt.Sprintf("tenant %q already exists", tenant)
}

// validateSpec checks what the caller controls. The name becomes an
// object name and seeds a workspace name, so it has to be a DNS label.
func validateSpec(spec *tenancyv1alpha1.SelfTenantRequestSpec) field.ErrorList {
	var errs field.ErrorList
	path := field.NewPath("spec")

	if spec.Name == "" {
		errs = append(errs, field.Required(path.Child("name"), "the tenant's name"))
		return errs
	}
	for _, msg := range validation.NameIsDNSLabel(spec.Name, false) {
		errs = append(errs, field.Invalid(path.Child("name"), spec.Name, msg))
	}
	return errs
}
