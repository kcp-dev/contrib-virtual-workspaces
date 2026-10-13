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

// Phase describes where a tenancy object is in its lifecycle. It is a
// coarse, human-first signal; Message carries the detail when a phase is
// not Ready.
type Phase string

const (
	// PhasePending means the object has been observed but its backing
	// workspace or RBAC has not materialized yet.
	PhasePending Phase = "Pending"
	// PhaseReady means everything the object asks for exists and is usable.
	PhaseReady Phase = "Ready"
	// PhaseError means the last reconciliation failed; Message says why.
	PhaseError Phase = "Error"
)

// Role is the access level a Membership grants inside a tenant or project
// workspace. Roles are fixed rather than free-form references so that a
// Membership is reviewable at a glance and portable between deployments.
type Role string

const (
	// RoleView grants read access to everything in the workspace.
	RoleView Role = "view"
	// RoleEdit grants read and write access to everything in the workspace,
	// except RBAC itself.
	RoleEdit Role = "edit"
	// RoleAdmin grants unrestricted access in the workspace.
	RoleAdmin Role = "admin"
)

// SubjectKind says how a Membership subject name is matched against the
// calling identity.
type SubjectKind string

const (
	// SubjectKindUser matches the authenticated username verbatim.
	SubjectKindUser SubjectKind = "User"
	// SubjectKindGroup matches any of the authenticated identity's groups.
	SubjectKindGroup SubjectKind = "Group"
)

// Subject identifies who a Membership is for.
type Subject struct {
	// Kind is User or Group.
	Kind SubjectKind `json:"kind"`
	// Name is the username or group name, compared verbatim against the
	// identity kcp authenticates. No issuer prefixes are added or stripped
	// here; the deployment's authentication configuration must agree with
	// kcp's.
	Name string `json:"name"`
}

// Tenant asks for an isolated kcp workspace for one organizational unit.
// The operator provisions a workspace under the organization workspace the
// Tenant object lives in and reports it in Status.
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type Tenant struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec TenantSpec `json:"spec"`
	// +optional
	Status TenantStatus `json:"status,omitempty"`
}

// TenantSpec is the desired state of a Tenant.
type TenantSpec struct {
	// DisplayName is the human name of the tenant. It seeds the workspace
	// name (subject to the operator's naming strategy) and is returned in
	// SelfTenancyReview answers. It is not required to be unique.
	DisplayName string `json:"displayName"`
	// Description is free text for humans.
	// +optional
	Description string `json:"description,omitempty"`
}

// TenantStatus is the observed state of a Tenant.
type TenantStatus struct {
	// +optional
	Phase Phase `json:"phase,omitempty"`
	// +optional
	Message string `json:"message,omitempty"`
	// Workspace is the name of the provisioned Workspace object, in the
	// same workspace the Tenant lives in.
	// +optional
	Workspace string `json:"workspace,omitempty"`
	// WorkspaceCluster is the logical cluster name backing the provisioned
	// workspace. It is the stable address of the tenant's workspace.
	// +optional
	WorkspaceCluster string `json:"workspaceCluster,omitempty"`
	// URL is the direct address of the provisioned workspace.
	// +optional
	URL string `json:"url,omitempty"`
}

// TenantList is the list form of Tenant.
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type TenantList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []Tenant `json:"items"`
}

// Project asks for a workspace nested under a tenant's workspace. Projects
// are the unit teams actually work in; tenant-wide Memberships reach every
// project, project Memberships reach one.
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type Project struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec ProjectSpec `json:"spec"`
	// +optional
	Status ProjectStatus `json:"status,omitempty"`
}

// ProjectSpec is the desired state of a Project.
type ProjectSpec struct {
	// Tenant names the Tenant (in the same workspace) this project belongs
	// to. The project's workspace is created under that tenant's workspace.
	Tenant string `json:"tenant"`
	// DisplayName is the human name of the project; it seeds the workspace
	// name the same way a tenant's does.
	DisplayName string `json:"displayName"`
	// Description is free text for humans.
	// +optional
	Description string `json:"description,omitempty"`
}

// ProjectStatus is the observed state of a Project.
type ProjectStatus struct {
	// +optional
	Phase Phase `json:"phase,omitempty"`
	// +optional
	Message string `json:"message,omitempty"`
	// Workspace is the name of the provisioned Workspace object, inside the
	// tenant's workspace.
	// +optional
	Workspace string `json:"workspace,omitempty"`
	// WorkspaceCluster is the logical cluster name backing the provisioned
	// workspace.
	// +optional
	WorkspaceCluster string `json:"workspaceCluster,omitempty"`
	// URL is the direct address of the provisioned workspace.
	// +optional
	URL string `json:"url,omitempty"`
}

// ProjectList is the list form of Project.
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type ProjectList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []Project `json:"items"`
}

// Membership grants one subject one role in one tenant, or in one project
// of that tenant. The operator materializes it as RBAC in the target
// workspace; deleting the Membership removes that RBAC again.
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type Membership struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec MembershipSpec `json:"spec"`
	// +optional
	Status MembershipStatus `json:"status,omitempty"`
}

// MembershipSpec is the desired state of a Membership.
type MembershipSpec struct {
	// Subject is who the grant is for.
	Subject Subject `json:"subject"`
	// Role is the access level granted: view, edit or admin.
	Role Role `json:"role"`
	// Tenant names the Tenant (in the same workspace) the grant applies to.
	Tenant string `json:"tenant"`
	// Project optionally narrows the grant to one project of the tenant.
	// Empty means the grant applies to the tenant's workspace, which every
	// project workspace nests under.
	// +optional
	Project string `json:"project,omitempty"`
}

// MembershipStatus is the observed state of a Membership.
type MembershipStatus struct {
	// +optional
	Phase Phase `json:"phase,omitempty"`
	// +optional
	Message string `json:"message,omitempty"`
}

// MembershipList is the list form of Membership.
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type MembershipList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []Membership `json:"items"`
}

// SelfTenancyReview answers "which tenants and projects am I a member of,
// and as what role?". Modelled on Kubernetes SelfSubjectReview: the caller
// POSTs a mostly empty object and the server fills Status from the
// authenticated identity. There is no Spec; the only input is who is
// asking.
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type SelfTenancyReview struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`
	// +optional
	Status SelfTenancyReviewStatus `json:"status,omitempty"`
}

// SelfTenancyReviewStatus lists the caller's tenants, sorted by name for
// stable output.
type SelfTenancyReviewStatus struct {
	// +optional
	Tenants []TenantClaim `json:"tenants,omitempty"`
}

// TenantClaim is one tenant the caller belongs to.
type TenantClaim struct {
	// Name is the Tenant object's name.
	Name string `json:"name"`
	// DisplayName is the tenant's human name.
	// +optional
	DisplayName string `json:"displayName,omitempty"`
	// Cluster is the logical cluster of the tenant's workspace.
	// +optional
	Cluster string `json:"cluster,omitempty"`
	// Endpoint is the front-proxy URL the caller can address the tenant's
	// workspace at.
	// +optional
	Endpoint string `json:"endpoint,omitempty"`
	// Roles are the roles the caller holds tenant-wide, sorted and
	// deduplicated.
	// +optional
	Roles []string `json:"roles,omitempty"`
	// Projects are the tenant's projects the caller can reach, either
	// through a tenant-wide role or a project-scoped one.
	// +optional
	Projects []ProjectClaim `json:"projects,omitempty"`
}

// ProjectClaim is one project the caller can reach.
type ProjectClaim struct {
	// Name is the Project object's name.
	Name string `json:"name"`
	// DisplayName is the project's human name.
	// +optional
	DisplayName string `json:"displayName,omitempty"`
	// Cluster is the logical cluster of the project's workspace.
	// +optional
	Cluster string `json:"cluster,omitempty"`
	// Endpoint is the front-proxy URL the caller can address the project's
	// workspace at.
	// +optional
	Endpoint string `json:"endpoint,omitempty"`
	// Roles are the roles the caller holds in this project, including
	// tenant-wide ones, sorted and deduplicated.
	// +optional
	Roles []string `json:"roles,omitempty"`
}

// SelfTenancyReviewList is the list form of SelfTenancyReview. Review
// resources are not listed; the type exists so the resource can be
// registered with a scheme without special-casing.
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type SelfTenancyReviewList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []SelfTenancyReview `json:"items"`
}
