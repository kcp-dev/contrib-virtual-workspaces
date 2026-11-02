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
	"k8s.io/apimachinery/pkg/runtime"
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

// ProducerMode says who owns the producer's workspace.
type ProducerMode string

const (
	// ProducerModeProvisioned means the operator creates the producer's
	// workspace under the producers parent, with the `producer`
	// WorkspaceType carrying the publish binding.
	ProducerModeProvisioned ProducerMode = "provisioned"
	// ProducerModeExternal means the producer brings their own workspace
	// (anywhere in kcp) and consents by binding marketplace-publish there
	// themselves.
	ProducerModeExternal ProducerMode = "external"
)

// Producer is the registration record: it says which workspace is allowed
// to publish offerings under this name. It lives in the catalog workspace
// and grants nothing by existing — the reach into a producer workspace
// comes from the marketplace-publish binding there, never from this
// record.
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:subresource:status
type Producer struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec ProducerSpec `json:"spec"`
	// +optional
	Status ProducerStatus `json:"status,omitempty"`
}

// ProducerSpec is the desired state of a Producer.
type ProducerSpec struct {
	// DisplayName is the human name of the producer, shown in the catalog.
	// +kubebuilder:validation:MinLength=1
	DisplayName string `json:"displayName"`
	// Description is free text for humans.
	// +optional
	Description string `json:"description,omitempty"`
	// Mode is provisioned or external.
	// +kubebuilder:validation:Enum=provisioned;external
	Mode ProducerMode `json:"mode"`
	// WorkspacePath is the canonical path of the producer's own workspace,
	// for mode external. Ignored for mode provisioned, where the operator
	// computes it.
	// +optional
	WorkspacePath string `json:"workspacePath,omitempty"`
}

// ProducerStatus is the observed state of a Producer.
type ProducerStatus struct {
	// +optional
	Phase Phase `json:"phase,omitempty"`
	// +optional
	Message string `json:"message,omitempty"`
	// Workspace is the name of the provisioned Workspace object, for mode
	// provisioned.
	// +optional
	Workspace string `json:"workspace,omitempty"`
	// WorkspacePath is the canonical path of the producer's workspace —
	// computed for provisioned, copied from spec for external. Offerings
	// bind against it.
	// +optional
	WorkspacePath string `json:"workspacePath,omitempty"`
	// WorkspaceCluster is the logical cluster backing the producer's
	// workspace. For external producers it is learned when the first
	// Offering is published from that workspace.
	// +optional
	WorkspaceCluster string `json:"workspaceCluster,omitempty"`
	// URL is the direct address of the producer's workspace, when known.
	// +optional
	URL string `json:"url,omitempty"`
}

// ProducerList is the list form of Producer.
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:object:root=true
type ProducerList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []Producer `json:"items"`
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
	// MetadataDomain prefixes the marketplace's label and annotation keys.
	MetadataDomain = "marketplace.kcp.hub"

	// AnnotationPublish opts an APIExport into the catalog. Its value must
	// be "true"; anything else, or its absence, means not published.
	//
	// Publishing is the producer's own act, on their own object, in their
	// own workspace — and removing this annotation revokes the `bind`
	// grant, which is what takes an offering off the catalog.
	AnnotationPublish = MetadataDomain + "/publish"

	// AnnotationDisplayName is the human name shown in the catalog.
	// Absent, the export's own name is used.
	AnnotationDisplayName = MetadataDomain + "/display-name"

	// AnnotationDescription is free text for humans.
	AnnotationDescription = MetadataDomain + "/description"

	// AnnotationDocsURL links to the producer's own documentation.
	AnnotationDocsURL = MetadataDomain + "/docs-url"

	// AnnotationInstructions names a ConfigMap in the same workspace as the
	// export whose keys are sections of "how to use this" — one per client
	// or scenario, written by the producer, shown to a consumer after they
	// enable it. The content is static text the console renders, with a
	// small set of {{variables}} it fills in: the server, the consumer's
	// workspace, their identity. See the marketplace README for the list.
	//
	// A ConfigMap rather than more annotations because setup instructions
	// for several clients are paragraphs and code blocks, not a line —
	// and a ConfigMap is the one core object made for exactly that.
	AnnotationInstructions = MetadataDomain + "/instructions"
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
	// MatchLabels is the scope the producer suggests for this claim, from
	// the claim's defaultSelector. Empty means the claim covers every
	// object of that resource, which is what a consumer accepts by
	// default.
	// +optional
	MatchLabels map[string]string `json:"matchLabels,omitempty"`
}

// CatalogOffering is the virtual, read-only view of one offering as the
// marketplace virtual workspace serves it: a published APIExport joined
// with the Producer record whose workspace it lives in, named
// "<producer>.<export>". It carries everything needed to author the
// APIBinding that enabling means — the export, the workspace path it lives
// at, and its claims, identity hashes included. Reading the catalog is
// reading the consent form.
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type CatalogOffering struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Producer is the catalog Producer record's name.
	Producer string `json:"producer"`
	// Offering is the name of the published APIExport in the producer's
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
	// DocsURL links to the producer's own documentation, if they gave one.
	// +optional
	DocsURL string `json:"docsURL,omitempty"`
	// Instructions is the producer's "how to use this", keyed by section.
	// Static text with {{variables}} the console fills in; see
	// AnnotationInstructions.
	// +optional
	Instructions map[string]string `json:"instructions,omitempty"`
	// Resources is the API enabling adds: each resource the export serves,
	// with the schema its APIResourceSchema declares, so a consumer can
	// read what they would get before enabling it. A resource whose schema
	// has not been seen is listed without versions.
	// +optional
	Resources []OfferingResource `json:"resources,omitempty"`
}

// OfferingResource is one resource an offering serves, projected from the
// APIResourceSchema the export references.
type OfferingResource struct {
	// Group and Resource name it as the export does.
	// +optional
	Group    string `json:"group,omitempty"`
	Resource string `json:"resource"`
	// Schema is the APIResourceSchema's name in the producer's workspace.
	// +optional
	Schema string `json:"schema,omitempty"`
	// Kind and Scope are the schema's.
	// +optional
	Kind string `json:"kind,omitempty"`
	// +optional
	Scope string `json:"scope,omitempty"`
	// Versions are the schema's versions, in its order.
	// +optional
	Versions []OfferingResourceVersion `json:"versions,omitempty"`
}

// OfferingResourceVersion is one version of an offered resource.
type OfferingResourceVersion struct {
	Name    string `json:"name"`
	Served  bool   `json:"served"`
	Storage bool   `json:"storage"`
	// Schema is the version's OpenAPI v3 schema, verbatim.
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	Schema runtime.RawExtension `json:"schema,omitempty"`
}

// CatalogOfferingList is the list form of CatalogOffering.
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type CatalogOfferingList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []CatalogOffering `json:"items"`
}
