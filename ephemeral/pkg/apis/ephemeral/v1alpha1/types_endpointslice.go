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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EphemeralResourceEndpointSlice tells kcp where the virtual workspace serving
// an APIExport's ephemeral resources can be reached.
//
// An APIExport points at one of these through
// spec.resources[].storage.virtual.reference, and the shard reads the URL out
// of its status. It exists because the alternative -- referencing an
// APIExportEndpointSlice -- means accepting a URL kcp composes from
// shard.spec.virtualWorkspaceURL, which is a shard-global setting: pointing it
// at this server redirects every virtual workspace on the shard.
//
// The provider owns this object because the provider is the only party that
// knows where their virtual workspace runs.
//
// +kubebuilder:resource:scope=Cluster,categories=kcp
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
type EphemeralResourceEndpointSlice struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec EphemeralResourceEndpointSliceSpec `json:"spec,omitempty"`

	// +optional
	Status EphemeralResourceEndpointSliceStatus `json:"status,omitempty"`
}

type EphemeralResourceEndpointSliceSpec struct {
	// export names the APIExport whose ephemeral resources this virtual
	// workspace serves. It has to live in the same workspace as this object:
	// a provider publishes endpoints for their own exports.
	//
	// The name is what the served URL is built from, so it has to match the
	// APIExport that references this slice.
	//
	// +required
	Export EphemeralResourceEndpointSliceExport `json:"export"`
}

type EphemeralResourceEndpointSliceExport struct {
	// name of the APIExport.
	//
	// +kubebuilder:validation:MinLength=1
	// +required
	Name string `json:"name"`
}

type EphemeralResourceEndpointSliceStatus struct {
	// endpoints are the URLs this export's ephemeral resources are served on,
	// filled in by the virtual workspace itself.
	//
	// The shape is kcp's: a url, and optionally a shards selector saying which
	// shards should use it. This server publishes a single endpoint with an
	// empty selector -- one virtual workspace, every shard -- because that is
	// what a webhook-backed resource looks like: there is nothing shard-local
	// about asking a provider a question.
	//
	// +optional
	// +listType=atomic
	Endpoints []EphemeralResourceEndpoint `json:"endpoints,omitempty"`

	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// EphemeralResourceEndpoint is one URL and the shards it serves.
//
// This mirrors core.kcp.io/v1alpha1 Endpoint, which kcp reads out of any kind
// an APIExport references -- the shape is a contract, not a Go type, since the
// shard reads it unstructured. It is duplicated here only because the released
// SDK does not carry that type yet; once it does this should become
//
//	type EphemeralResourceEndpoint = corev1alpha1.Endpoint
//
// which is what kcp's own APIExportEndpoint already is, and what stops the two
// drifting apart.
type EphemeralResourceEndpoint struct {
	// url is where this export's ephemeral resources are served.
	//
	// +kubebuilder:validation:MinLength=1
	// +required
	URL string `json:"url"`

	// shards says which shards should use this URL.
	//
	// Saying nothing -- the zero value -- leaves the URL to be matched by
	// prefix against the shard's own address, which is what kcp's controllers
	// publish and not what a provider-run virtual workspace wants.
	//
	// +optional
	Shards EndpointSelector `json:"shards,omitempty"`
}

// EndpointSelector says which shards an endpoint URL serves.
//
// matchAll and selector are mutually exclusive: matchAll is the whole
// installation, selector is a subset of it.
//
// TODO: Move this to core.kcp.io/v1alpha1 and have the SDK carry it, so that EphemeralResourceEndpoint can be a type alias.
//
// +kubebuilder:validation:XValidation:rule="!(has(self.matchAll) && self.matchAll && has(self.selector))",message="matchAll and selector are mutually exclusive"
type EndpointSelector struct {
	// matchAll says this URL serves every shard: one virtual workspace for the
	// whole installation, which is what a single ephemeral virtual workspace
	// publishes. There is nothing shard-local about asking a provider's webhook
	// a question.
	//
	// +optional
	MatchAll bool `json:"matchAll,omitempty"`

	// selector picks the shards this URL serves by matching against shard
	// labels. Every shard labels itself with its own name, so one shard is
	// selected with matchLabels: {name: <shard>}, and a region uses whatever
	// labels the installation puts on its shards.
	//
	// +optional
	Selector *metav1.LabelSelector `json:"selector,omitempty"`
}

// EphemeralResourceEndpointSliceList is a list of EphemeralResourceEndpointSlices.
//
// +kubebuilder:object:root=true
type EphemeralResourceEndpointSliceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []EphemeralResourceEndpointSlice `json:"items"`
}

const (
	// EndpointSliceResource is the plural resource name.
	EndpointSliceResource = "ephemeralresourceendpointslices"

	// EndpointSliceKind is the kind an APIExport references.
	EndpointSliceKind = "EphemeralResourceEndpointSlice"

	// ConditionEndpointsPublished says whether this server has put its address
	// in the status.
	ConditionEndpointsPublished = "EndpointsPublished"
)
