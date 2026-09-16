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

// Package selftenancyreview implements the SelfTenancyReview API of the
// tenancy virtual workspace.
package selftenancyreview

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/apiserver/pkg/registry/rest"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/contrib-virtual-workspaces/tenancy/pkg/directory"
	tenancyv1alpha1 "github.com/kcp-dev/contrib-virtual-workspaces/tenancy/sdk/apis/tenancy/v1alpha1"
)

// REST answers SelfTenancyReview creates from the directory.
type REST struct {
	directory   *directory.Directory
	endpointFor func(cluster string) string
}

var (
	_ rest.Storage              = &REST{}
	_ rest.Creater              = &REST{}
	_ rest.Scoper               = &REST{}
	_ rest.SingularNameProvider = &REST{}
)

// NewREST returns the REST storage backed by the given directory.
// endpointFor renders a workspace cluster name into the URL callers reach
// it at.
func NewREST(d *directory.Directory, endpointFor func(cluster string) string) *REST {
	return &REST{directory: d, endpointFor: endpointFor}
}

// New returns an empty SelfTenancyReview.
func (r *REST) New() runtime.Object {
	return &tenancyv1alpha1.SelfTenancyReview{}
}

// Destroy is a no-op; the storage holds no resources that need cleanup.
func (r *REST) Destroy() {}

// NamespaceScoped reports that the resource is cluster-scoped.
func (r *REST) NamespaceScoped() bool {
	return false
}

// GetSingularName returns the singular resource name.
func (r *REST) GetSingularName() string {
	return "selftenancyreview"
}

// Create answers the review: the caller comes from the request context,
// the answer from the directory.
func (r *REST) Create(ctx context.Context, obj runtime.Object, _ rest.ValidateObjectFunc, _ *metav1.CreateOptions) (runtime.Object, error) {
	review, ok := obj.(*tenancyv1alpha1.SelfTenancyReview)
	if !ok {
		return nil, apierrors.NewBadRequest(fmt.Sprintf("not a SelfTenancyReview: %T", obj))
	}

	if !r.directory.Ready() {
		return nil, apierrors.NewServiceUnavailable("tenancy directory is not ready; try again shortly")
	}

	user, ok := genericapirequest.UserFrom(ctx)
	if !ok {
		return nil, apierrors.NewUnauthorized("no user present in request context")
	}

	tenants := r.directory.ReviewFor(user.GetName(), user.GetGroups(), r.endpointFor)

	klog.FromContext(ctx).V(4).Info("answered SelfTenancyReview",
		"username", user.GetName(),
		"groups", user.GetGroups(),
		"tenants", len(tenants),
	)

	out := review.DeepCopy()
	out.Status = tenancyv1alpha1.SelfTenancyReviewStatus{Tenants: tenants}
	return out, nil
}
