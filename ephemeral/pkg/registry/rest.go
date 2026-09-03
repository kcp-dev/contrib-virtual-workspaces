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

// Package registry implements the REST storage that answers a create by
// calling a provider webhook. Nothing is persisted.
package registry

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"

	authenticationv1 "k8s.io/api/authentication/v1"
	apiservervalidation "k8s.io/apiextensions-apiserver/pkg/apiserver/validation"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	utiljson "k8s.io/apimachinery/pkg/util/json"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/apiserver/pkg/registry/rest"
	"k8s.io/klog/v2"
	"sigs.k8s.io/structured-merge-diff/v6/fieldpath"

	ephemeralv1alpha1 "github.com/kcp-dev/contrib-virtual-workspaces/ephemeral/pkg/apis/ephemeral/v1alpha1"
	"github.com/kcp-dev/contrib-virtual-workspaces/ephemeral/pkg/webhook"
)

// The storage implements exactly these interfaces and no more. Discovery in the
// virtual workspace framework derives the advertised verbs by type-asserting
// the storage, so *not* implementing rest.Lister, rest.Watcher and rest.Getter
// is what makes `kubectl get` disappear on the consumer side. Do not add them.
var (
	_ rest.Storage              = (*REST)(nil)
	_ rest.Creater              = (*REST)(nil)
	_ rest.Scoper               = (*REST)(nil)
	_ rest.SingularNameProvider = (*REST)(nil)
	_ rest.ResetFieldsStrategy  = (*REST)(nil)
	_ rest.TableConvertor       = (*REST)(nil)
)

// REST serves one ephemeral resource by delegating create to a webhook.
type REST struct {
	gvr             schema.GroupVersionResource
	gvk             schema.GroupVersionKind
	singularName    string
	namespaceScoped bool

	tableConvertor rest.TableConvertor

	// validator validates the object the webhook returns, so that a broken or
	// hostile webhook cannot smuggle arbitrary content through a typed API.
	validator apiservervalidation.SchemaValidator

	client *webhook.Client
}

// New builds a REST storage for one resource.
func New(
	gvr schema.GroupVersionResource,
	gvk schema.GroupVersionKind,
	singularName string,
	namespaceScoped bool,
	tableConvertor rest.TableConvertor,
	validator apiservervalidation.SchemaValidator,
	client *webhook.Client,
) *REST {
	return &REST{
		gvr:             gvr,
		gvk:             gvk,
		singularName:    singularName,
		namespaceScoped: namespaceScoped,
		tableConvertor:  tableConvertor,
		validator:       validator,
		client:          client,
	}
}

func (r *REST) New() runtime.Object {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(r.gvk)
	return obj
}

func (r *REST) Destroy() {}

func (r *REST) NamespaceScoped() bool { return r.namespaceScoped }

func (r *REST) GetSingularName() string { return r.singularName }

// GetResetFields is required by the framework's CreateServingInfoFor, which
// refuses to build a serving info for a storage without it. Nothing is stored,
// so no field is ever reset.
func (r *REST) GetResetFields() map[fieldpath.APIVersion]*fieldpath.Set {
	return map[fieldpath.APIVersion]*fieldpath.Set{}
}

func (r *REST) ConvertToTable(ctx context.Context, object runtime.Object, tableOptions runtime.Object) (*metav1.Table, error) {
	if r.tableConvertor == nil {
		return rest.NewDefaultTableConvertor(r.gvr.GroupResource()).ConvertToTable(ctx, object, tableOptions)
	}
	return r.tableConvertor.ConvertToTable(ctx, object, tableOptions)
}

// Create answers the request from the webhook. The returned object is written
// to the client as the 201 body and is never persisted.
func (r *REST) Create(ctx context.Context, obj runtime.Object, createValidation rest.ValidateObjectFunc, options *metav1.CreateOptions) (runtime.Object, error) {
	logger := klog.FromContext(ctx).WithValues("resource", r.gvr.String(), "webhook", r.client.URL())

	u, ok := obj.(*unstructured.Unstructured)
	if !ok {
		return nil, apierrors.NewInternalError(fmt.Errorf("expected *unstructured.Unstructured, got %T", obj))
	}

	// Run admission before anything leaves the process.
	if createValidation != nil {
		if err := createValidation(ctx, obj.DeepCopyObject()); err != nil {
			return nil, err
		}
	}

	cluster, err := genericapirequest.ValidClusterFrom(ctx)
	if err != nil {
		return nil, apierrors.NewInternalError(fmt.Errorf("no logical cluster in context: %w", err))
	}
	if cluster.Wildcard {
		return nil, apierrors.NewBadRequest("ephemeral resources cannot be created against a wildcard cluster")
	}

	namespace, _ := genericapirequest.NamespaceFrom(ctx)

	raw, err := json.Marshal(u.Object)
	if err != nil {
		return nil, apierrors.NewInternalError(fmt.Errorf("marshalling submitted object: %w", err))
	}

	review := &ephemeralv1alpha1.EphemeralReview{
		TypeMeta: metav1.TypeMeta{
			APIVersion: ephemeralv1alpha1.SchemeGroupVersion,
			Kind:       ephemeralv1alpha1.EphemeralReviewKind,
		},
		Request: &ephemeralv1alpha1.EphemeralRequest{
			UID:     types.UID(uuid.New().String()),
			Cluster: cluster.Name.String(),
			Resource: metav1.GroupVersionResource{
				Group:    r.gvr.Group,
				Version:  r.gvr.Version,
				Resource: r.gvr.Resource,
			},
			Kind: metav1.GroupVersionKind{
				Group:   r.gvk.Group,
				Version: r.gvk.Version,
				Kind:    r.gvk.Kind,
			},
			Namespace: namespace,
			Name:      u.GetName(),
			UserInfo:  userInfoFrom(ctx),
			DryRun:    len(options.DryRun) > 0,
			Object:    runtime.RawExtension{Raw: raw},
		},
	}

	response, err := r.client.Call(ctx, review)
	if err != nil {
		// The webhook is unreachable, timed out, or answered garbage. There is
		// no "ignore" failure policy: an ephemeral resource whose webhook did
		// not answer has no value, and returning the submitted object unchanged
		// would be indistinguishable from a real, empty answer.
		logger.Error(err, "ephemeral webhook call failed")
		return nil, apierrors.NewServiceUnavailable(fmt.Sprintf("ephemeral webhook for %s did not answer: %v", r.gvr.GroupResource(), err))
	}

	if !response.Allowed {
		return nil, errorFromStatus(r.gvr.GroupResource(), u.GetName(), response.Status)
	}

	if response.Object == nil || len(response.Object.Raw) == 0 {
		return nil, apierrors.NewInternalError(fmt.Errorf("ephemeral webhook for %s allowed the request but returned no object", r.gvr.GroupResource()))
	}

	out := &unstructured.Unstructured{}
	// utiljson, not encoding/json: it converts whole numbers to int64 rather
	// than float64, which is the contract unstructured objects are read under
	// everywhere else (NestedInt64, the schema validator, printers).
	if err := utiljson.Unmarshal(response.Object.Raw, &out.Object); err != nil {
		return nil, apierrors.NewInternalError(fmt.Errorf("ephemeral webhook for %s returned an undecodable object: %w", r.gvr.GroupResource(), err))
	}

	// The webhook answers in the version it was asked about; it does not get to
	// change the identity of the object.
	out.SetGroupVersionKind(r.gvk)

	if errs := apiservervalidation.ValidateCustomResource(nil, out.Object, r.validator); len(errs) > 0 {
		logger.Error(nil, "ephemeral webhook returned a non-conforming object", "errors", errs.ToAggregate().Error())
		return nil, apierrors.NewInternalError(fmt.Errorf("ephemeral webhook for %s returned an object that does not conform to the schema: %w",
			r.gvr.GroupResource(), errs.ToAggregate()))
	}

	if !r.namespaceScoped {
		out.SetNamespace("")
	} else if out.GetNamespace() == "" {
		out.SetNamespace(namespace)
	}

	return out, nil
}

// errorFromStatus turns a webhook's denial into a proper API error, so that a
// provider can say "no such bucket" as a 404 rather than an opaque failure.
func errorFromStatus(gr schema.GroupResource, name string, status *metav1.Status) error {
	if status == nil {
		return apierrors.NewForbidden(gr, name, fmt.Errorf("denied by the ephemeral webhook"))
	}

	code := status.Code
	if code == 0 {
		code = 403
	}

	out := status.DeepCopy()
	out.Status = metav1.StatusFailure
	out.Code = code
	if out.Reason == "" {
		out.Reason = apierrors.NewGenericServerResponse(int(code), "create", gr, name, "", 0, false).Status().Reason
	}
	if out.Message == "" {
		out.Message = fmt.Sprintf("the ephemeral webhook denied the request for %s", gr)
	}

	return &apierrors.StatusError{ErrStatus: *out}
}

func userInfoFrom(ctx context.Context) authenticationv1.UserInfo {
	info, ok := genericapirequest.UserFrom(ctx)
	if !ok {
		return authenticationv1.UserInfo{}
	}

	extra := map[string]authenticationv1.ExtraValue{}
	for k, v := range info.GetExtra() {
		extra[k] = authenticationv1.ExtraValue(v)
	}
	if len(extra) == 0 {
		extra = nil
	}

	return authenticationv1.UserInfo{
		Username: info.GetName(),
		UID:      info.GetUID(),
		Groups:   info.GetGroups(),
		Extra:    extra,
	}
}
