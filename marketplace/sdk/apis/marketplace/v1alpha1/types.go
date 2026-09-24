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

// OfferingClaim is one tenant-side permission claim an offering carries:
// what a consumer accepts when they enable it. The operator validates
// every claim against the provider's real APIExport and records the
// intersection — declaring here grants nothing.
type OfferingClaim struct {
	// Group and Resource name what is claimed, as kcp permission claims do.
	// +optional
	Group    string `json:"group,omitempty"`
	Resource string `json:"resource"`
	// Verbs are the verbs claimed. They come from the declaration, never
	// from an enablement request.
	Verbs []string `json:"verbs"`
	// IdentityHash is required when the claimed resource is itself served
	// by an APIExport. The operator fills it from the provider's export;
	// setting it here is not trusted.
	// +optional
	IdentityHash string `json:"identityHash,omitempty"`
	// MatchLabels narrows the claim to objects carrying these labels.
	// Empty means the claim covers all objects (matchAll). A claim on a
	// sensitive core resource such as secrets should never be unscoped.
	// +optional
	MatchLabels map[string]string `json:"matchLabels,omitempty"`
}

// Offering is the catalog card: one orderable unit, published by a
// provider in its own workspace, next to the APIExport it references.
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:subresource:status
type Offering struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec OfferingSpec `json:"spec"`
	// +optional
	Status OfferingStatus `json:"status,omitempty"`
}

// OfferingSpec is the desired state of an Offering.
type OfferingSpec struct {
	// DisplayName is the human name of the offering, shown in the catalog.
	DisplayName string `json:"displayName"`
	// Description is free text for humans.
	// +optional
	Description string `json:"description,omitempty"`
	// Export names the APIExport, in this same workspace, that enabling
	// this offering binds.
	// +kubebuilder:validation:MinLength=1
	Export string `json:"export"`
	// Claims are the tenant-side permission claims consumers accept when
	// enabling. Each must be declared on the export too; the operator
	// records the validated intersection in status.
	// +optional
	Claims []OfferingClaim `json:"claims,omitempty"`
}

// OfferingStatus is the observed state of an Offering.
type OfferingStatus struct {
	// +optional
	Phase Phase `json:"phase,omitempty"`
	// +optional
	Message string `json:"message,omitempty"`
	// Provider is the catalog Provider record this workspace publishes
	// under, resolved by the operator from the workspace's canonical path.
	// +optional
	Provider string `json:"provider,omitempty"`
	// IdentityHash is the identity of the referenced export.
	// +optional
	IdentityHash string `json:"identityHash,omitempty"`
	// Revision counts changes to the applied claim surface. An Enablement
	// accepts a revision; a widened offering waits at a new revision until
	// consumers accept it.
	// +optional
	Revision int64 `json:"revision,omitempty"`
	// AppliedClaims is the validated claim set at Revision: the
	// intersection of spec.claims with what the export really declares.
	// This — never spec — is what an enablement materializes.
	// +optional
	AppliedClaims []OfferingClaim `json:"appliedClaims,omitempty"`
}

// OfferingList is the list form of Offering.
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:object:root=true
type OfferingList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []Offering `json:"items"`
}

// CatalogOffering is the virtual, read-only view of one offering as the
// marketplace virtual workspace serves it: the offering joined with its
// provider, named "<provider>.<offering>". It carries everything needed to
// author the APIBinding that enabling means — the export, the workspace
// path it lives at, and the claims at Revision, identity hashes included.
// Reading the catalog is reading the consent form.
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type CatalogOffering struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Provider is the catalog Provider record's name.
	Provider string `json:"provider"`
	// Offering is the Offering object's name in the provider's workspace.
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
	// Revision is the current claim-surface revision — the number an
	// Enablement's acceptedRevision must name.
	// +optional
	Revision int64 `json:"revision,omitempty"`
	// Claims is the validated claim surface at Revision.
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
