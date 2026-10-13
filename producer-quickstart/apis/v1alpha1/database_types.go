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

// Phase values a Database moves through.
const (
	PhasePending = "Pending"
	PhaseReady   = "Ready"
	PhaseError   = "Error"
)

// ConditionReady is the one condition this producer reports. Add more as
// the real provisioning grows states worth watching for.
const ConditionReady = "Ready"

// DatabaseSpec is what a consumer asks for.
type DatabaseSpec struct {
	// Engine selects the database engine.
	// +kubebuilder:validation:Enum=postgres;mysql
	// +kubebuilder:default=postgres
	// +optional
	Engine string `json:"engine,omitempty"`

	// Size is the instance class.
	// +kubebuilder:validation:Enum=small;medium;large
	// +kubebuilder:default=small
	// +optional
	Size string `json:"size,omitempty"`
}

// DatabaseStatus is what the producer reports back.
type DatabaseStatus struct {
	// Phase summarises where provisioning is: Pending, Ready or Error.
	// +optional
	Phase string `json:"phase,omitempty"`

	// Endpoint is where the database answers once it is Ready.
	// +optional
	Endpoint string `json:"endpoint,omitempty"`

	// Message says why, in a sentence, when the phase alone is not enough.
	// +optional
	Message string `json:"message,omitempty"`

	// ObservedGeneration is the spec generation this status describes, so a
	// reader can tell a stale Ready from a current one.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions follow the usual Kubernetes shape, for tooling that reads
	// them rather than the phase.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// Database is one database instance a consumer asked for in their
// workspace.
//
// Cluster-scoped: a kcp workspace is already the tenancy boundary, so a
// namespace inside it would be a second one nobody asked for. Namespaced
// kinds work too — drop the scope marker — and the console handles both.
//
// The printcolumn markers are the whole of making this kind presentable:
// kubectl and the console both ask kcp for a Table and show these columns,
// with no code in either that knows what a Database is. Columns with a
// priority are shown with -o wide, and in the console's detail view.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,categories=dbaas
// +kubebuilder:printcolumn:name="Engine",type=string,JSONPath=`.spec.engine`
// +kubebuilder:printcolumn:name="Size",type=string,JSONPath=`.spec.size`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Endpoint",type=string,JSONPath=`.status.endpoint`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:printcolumn:name="Message",type=string,JSONPath=`.status.message`,priority=1
type Database struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +optional
	Spec DatabaseSpec `json:"spec,omitempty"`
	// +optional
	Status DatabaseStatus `json:"status,omitempty"`
}

// DatabaseList is a list of Databases.
//
// +kubebuilder:object:root=true
type DatabaseList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Database `json:"items"`
}
