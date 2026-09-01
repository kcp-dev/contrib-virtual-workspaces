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

// Package v1alpha1 defines the wire contract between the ephemeral virtual
// workspace and a provider's webhook. It is modelled directly on
// admission.k8s.io/v1 AdmissionReview so that implementers can reuse the
// mental model and most of the plumbing.
package v1alpha1

import (
	authenticationv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
)

const (
	// GroupName is the API group of the review type.
	GroupName = "ephemeral.contrib.kcp.io"

	// Version is the API version of the review type.
	Version = "v1alpha1"

	// SchemeGroupVersion is the group/version string sent to and expected back
	// from the webhook.
	SchemeGroupVersion = GroupName + "/" + Version

	// EphemeralReviewKind is the kind of the request and response envelope.
	EphemeralReviewKind = "EphemeralReview"
)

// EphemeralReview is the envelope exchanged with the webhook. kcp POSTs it with
// Request set and expects it back with Response set. Exactly one of the two is
// meaningful in each direction.
type EphemeralReview struct {
	metav1.TypeMeta `json:",inline"`

	// Request describes the incoming create request. Set by the virtual
	// workspace, never by the webhook.
	//
	// +optional
	Request *EphemeralRequest `json:"request,omitempty"`

	// Response carries the answer. Set by the webhook, never by the virtual
	// workspace.
	//
	// +optional
	Response *EphemeralResponse `json:"response,omitempty"`
}

// EphemeralRequest is the question put to the webhook.
type EphemeralRequest struct {
	// UID uniquely identifies this request. The webhook must echo it back in
	// the response; a mismatch is treated as a webhook failure.
	UID types.UID `json:"uid"`

	// Cluster is the name of the logical cluster the request was made against,
	// i.e. the consumer's workspace, not the provider's.
	Cluster string `json:"cluster"`

	// Resource is the group/version/resource being created.
	Resource metav1.GroupVersionResource `json:"resource"`

	// Kind is the group/version/kind being created.
	Kind metav1.GroupVersionKind `json:"kind"`

	// Namespace is the namespace of the request, empty for cluster-scoped
	// resources.
	//
	// +optional
	Namespace string `json:"namespace,omitempty"`

	// Name is the name carried by the submitted object. Ephemeral resources are
	// not required to be named, so this is frequently empty.
	//
	// +optional
	Name string `json:"name,omitempty"`

	// UserInfo is the identity kcp authenticated. It is the webhook's primary
	// authorization input: kcp has already enforced RBAC on create for the
	// group/resource, anything finer is the webhook's decision.
	UserInfo authenticationv1.UserInfo `json:"userInfo"`

	// DryRun indicates the client asked for a dry run. A webhook that has side
	// effects must not perform them when this is true.
	//
	// +optional
	DryRun bool `json:"dryRun,omitempty"`

	// Object is the object the client submitted, in the requested version.
	Object runtime.RawExtension `json:"object"`
}

// EphemeralResponse is the webhook's answer.
type EphemeralResponse struct {
	// UID must equal the request's UID.
	UID types.UID `json:"uid"`

	// Allowed indicates whether an answer could be produced. When false, Status
	// carries the error returned to the client.
	Allowed bool `json:"allowed"`

	// Object is returned to the client as the 201 response body. It is
	// validated against the resource's OpenAPI schema before being written, so
	// a broken webhook cannot smuggle arbitrary content through a typed API.
	//
	// Required when Allowed is true.
	//
	// +optional
	Object *runtime.RawExtension `json:"object,omitempty"`

	// Status lets the webhook return a proper API error, e.g. a 404 for "no
	// such bucket" rather than an opaque failure. Only consulted when Allowed
	// is false.
	//
	// +optional
	Status *metav1.Status `json:"status,omitempty"`
}
