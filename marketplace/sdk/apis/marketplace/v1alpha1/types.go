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

// Phase describes where a marketplace object is in its lifecycle. It is a
// coarse, human-first signal; Message carries the detail when a phase is
// not Ready.
type Phase string

const (
	// PhasePending means the object has been observed but what it asks for
	// has not materialized yet.
	PhasePending Phase = "Pending"
	// PhaseReady means everything the object asks for exists and is usable.
	PhaseReady Phase = "Ready"
	// PhaseError means the last reconciliation failed; Message says why.
	PhaseError Phase = "Error"
)

// ProviderMode says who owns the provider's workspace.
type ProviderMode string

const (
	// ProviderModeProvisioned means the operator creates the provider's
	// workspace under the providers parent, with the `provider`
	// WorkspaceType carrying the publish binding.
	ProviderModeProvisioned ProviderMode = "provisioned"
	// ProviderModeExternal means the provider brings their own workspace
	// (anywhere in kcp) and consents by binding marketplace-publish there
	// themselves.
	ProviderModeExternal ProviderMode = "external"
)

// Provider is the registration record: it says which workspace is allowed
// to publish offerings under this name. It lives in the catalog workspace
// and grants nothing by existing — the reach into a provider workspace
// comes from the marketplace-publish binding there, never from this
// record.
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:subresource:status
type Provider struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec ProviderSpec `json:"spec"`
	// +optional
	Status ProviderStatus `json:"status,omitempty"`
}

// ProviderSpec is the desired state of a Provider.
type ProviderSpec struct {
	// DisplayName is the human name of the provider, shown in the catalog.
	// +kubebuilder:validation:MinLength=1
	DisplayName string `json:"displayName"`
	// Description is free text for humans.
	// +optional
	Description string `json:"description,omitempty"`
	// Mode is provisioned or external.
	// +kubebuilder:validation:Enum=provisioned;external
	Mode ProviderMode `json:"mode"`
	// WorkspacePath is the canonical path of the provider's own workspace,
	// for mode external. Ignored for mode provisioned, where the operator
	// computes it.
	// +optional
	WorkspacePath string `json:"workspacePath,omitempty"`
}

// ProviderStatus is the observed state of a Provider.
type ProviderStatus struct {
	// +optional
	Phase Phase `json:"phase,omitempty"`
	// +optional
	Message string `json:"message,omitempty"`
	// Workspace is the name of the provisioned Workspace object, for mode
	// provisioned.
	// +optional
	Workspace string `json:"workspace,omitempty"`
	// WorkspacePath is the canonical path of the provider's workspace —
	// computed for provisioned, copied from spec for external. Offerings
	// bind against it.
	// +optional
	WorkspacePath string `json:"workspacePath,omitempty"`
	// WorkspaceCluster is the logical cluster backing the provider's
	// workspace. For external providers it is learned when the first
	// Offering is published from that workspace.
	// +optional
	WorkspaceCluster string `json:"workspaceCluster,omitempty"`
	// URL is the direct address of the provider's workspace, when known.
	// +optional
	URL string `json:"url,omitempty"`
}

// ProviderList is the list form of Provider.
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:object:root=true
type ProviderList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []Provider `json:"items"`
}

// The annotations that publish an APIExport to the marketplace.
//
// An offering has no object of its own: an APIExport IS the offering, and
// these annotations are its catalog card. That is not only a saving of one
// kind — it removes the possibility of the two disagreeing. The old
// Offering named an export and restated its claims, so it could name an
// export that did not exist, or claim what the export did not, and the
// operator existed largely to detect and report those two cases. An
// annotation cannot be wrong about which export it is on, and the export's
// own spec.permissionClaims are the claim surface, so there is nothing
// left to reconcile them against.
const (
	// AnnotationPublish opts an APIExport into the catalog. Its value must
	// be "true"; anything else, or its absence, means not published.
	//
	// Publishing is the provider's own act, on their own object, in their
	// own workspace — and removing this annotation revokes the `bind`
	// grant, which is what takes an offering off the catalog.
	AnnotationPublish = GroupName + "/publish"

	// AnnotationDisplayName is the human name shown in the catalog.
	// Absent, the export's own name is used.
	AnnotationDisplayName = GroupName + "/display-name"

	// AnnotationDescription is free text for humans.
	AnnotationDescription = GroupName + "/description"
)

// OfferingClaim is one permission claim as the catalog shows it: what a
// consumer accepts when they enable the offering.
//
// It is a projection of the APIExport's own spec.permissionClaims, never a
// separate declaration — so what the catalog advertises and what the
// binding will carry cannot drift apart.
type OfferingClaim struct {
	// Group and Resource name what is claimed, as kcp permission claims do.
	// +optional
	Group    string `json:"group,omitempty"`
	Resource string `json:"resource"`
	// Verbs are the verbs claimed, as the export declares them.
	Verbs []string `json:"verbs"`
	// IdentityHash is set when the claimed resource is itself served by an
	// APIExport, copied from the claim.
	// +optional
	IdentityHash string `json:"identityHash,omitempty"`
	// MatchLabels is the scope the provider suggests for this claim, from
	// the claim's defaultSelector. Empty means the claim covers every
	// object of that resource, which is what a consumer accepts by
	// default.
	// +optional
	MatchLabels map[string]string `json:"matchLabels,omitempty"`
}

// CatalogOffering is the virtual, read-only view of one offering as the
// marketplace virtual workspace serves it: a published APIExport joined
// with the Provider record whose workspace it lives in, named
// "<provider>.<export>". It carries everything needed to author the
// APIBinding that enabling means — the export, the workspace path it lives
// at, and its claims, identity hashes included. Reading the catalog is
// reading the consent form.
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type CatalogOffering struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Provider is the catalog Provider record's name.
	Provider string `json:"provider"`
	// Offering is the name of the published APIExport in the provider's
	// workspace — the offering and the export are the same object.
	Offering string `json:"offering"`
	// DisplayName and Description mirror the offering's card.
	// +optional
	DisplayName string `json:"displayName,omitempty"`
	// +optional
	Description string `json:"description,omitempty"`
	// Export is the APIExport enabling binds, and WorkspacePath is the
	// canonical path of the workspace that owns it — together the
	// spec.reference.export of the APIBinding a consumer creates.
	// +optional
	Export string `json:"export,omitempty"`
	// +optional
	WorkspacePath string `json:"workspacePath,omitempty"`
	// Revision identifies the claim surface below: it is a digest of
	// Claims, so the same claims always produce the same revision and any
	// change produces a different one.
	//
	// It is derived rather than counted because there is no longer an
	// object to keep a counter on — which turns out to be the better
	// property anyway. A consumer who recorded the revision they accepted
	// can compare it with this one to see whether the consent form has
	// changed since, and the answer does not depend on how many times a
	// controller happened to observe it.
	// +optional
	Revision string `json:"revision,omitempty"`
	// Claims is the claim surface a consumer accepts by enabling: the
	// export's own permission claims, projected.
	// +optional
	Claims []OfferingClaim `json:"claims,omitempty"`
	// Ready reports whether enabling can currently succeed.
	Ready bool `json:"ready"`
}

// CatalogOfferingList is the list form of CatalogOffering.
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type CatalogOfferingList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []CatalogOffering `json:"items"`
}
