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

// Phase describes whether MCP is usable in this workspace.
type Phase string

const (
	// PhasePending means the binding exists but the grant MCP needs is not
	// in place yet.
	PhasePending Phase = "Pending"
	// PhaseReady means a client can connect and reach this workspace.
	PhaseReady Phase = "Ready"
	// PhaseError means the last reconciliation failed; Message says why.
	PhaseError Phase = "Error"
)

// MCPEndpoint reports that MCP is enabled in this workspace, and where to
// reach it.
//
// It carries no spec because there is nothing to ask for: enabling MCP is
// the APIBinding, and this object is what the operator writes back once
// that binding has let it grant itself the impersonation it needs. Reading
// it answers the two questions enabling raises — "did it work?" and "where
// do I point my client?" — without leaving the workspace.
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="URL",type=string,JSONPath=`.status.url`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:printcolumn:name="Message",type=string,JSONPath=`.status.message`,priority=1
type MCPEndpoint struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +optional
	Status MCPEndpointStatus `json:"status,omitempty"`
}

// MCPEndpointStatus is everything this kind has to say.
type MCPEndpointStatus struct {
	// +optional
	Phase Phase `json:"phase,omitempty"`
	// +optional
	Message string `json:"message,omitempty"`
	// URL is where an MCP client connects. It is the same address for
	// every workspace — which workspaces a session can see comes from the
	// caller's own access, not from the URL.
	// +optional
	URL string `json:"url,omitempty"`
	// Toolsets are the tool groups the server exposes.
	// +optional
	Toolsets []string `json:"toolsets,omitempty"`
}

// MCPEndpointList is the list form of MCPEndpoint.
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:object:root=true
type MCPEndpointList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []MCPEndpoint `json:"items"`
}
