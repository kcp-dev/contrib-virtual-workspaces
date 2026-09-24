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

// Package apiexport holds the kcp-side assets the tenancy component needs.
//
// The model is four APIExports across three tiers. Two carry schemas and
// two carry nothing but permission claims:
//
//   - tenancy-platform  Tenants. Bound into the store workspace.
//   - tenancy           Projects and Memberships. Bound into every tenant
//     workspace by the `tenant` WorkspaceType.
//   - tenancy-provisioner  no resources; the claim to create workspaces.
//     Bound where children are made: the tenants workspace and each
//     tenant workspace.
//   - tenancy-access    no resources; the claims to write RBAC and a
//     namespace inside a project workspace. Bound only into project
//     workspaces, by the `project` WorkspaceType.
//
// Splitting the two capabilities is the point: provisioning a workspace and
// reaching inside one are granted, and revoked, separately — and neither is
// ambient admin.
package apiexport

import "embed"

//go:embed *.yaml
var FS embed.FS

// Placeholders substituted at apply time. None of them can be committed:
// the identity hash is per kcp install, and the export references depend on
// where the operator chose to install.
const (
	// IdentityHashPlaceholder is replaced with the identity of kcp's own
	// tenancy.kcp.io APIExport, which a claim on `workspaces` must name.
	IdentityHashPlaceholder = "__TENANCY_KCP_IO_IDENTITY_HASH__"
	// ExportsRefPlaceholder is replaced with the logical cluster of the
	// workspace holding these exports; WorkspaceType references resolve
	// against it.
	ExportsRefPlaceholder = "__EXPORTS_REF__"
	// ExportsPathPlaceholder is replaced with that workspace's canonical
	// path. kcp compares limitAllowedParents against a Workspace's own
	// spec.type.path, which it always writes as a path, so a logical
	// cluster id there installs cleanly and only fails later.
	ExportsPathPlaceholder = "__EXPORTS_PATH__"
)

// ExportsOrder is the sequence the exports workspace's assets are applied
// in. It is load-bearing:
//
//   - schemas before the exports that name them, or the export is rejected;
//   - the bind RBAC before the endpoint slices, because kcp gates slice
//     creation on a `bind` verb the creator does not otherwise hold;
//   - the workspace types after the exports they default-bind, so the
//     references resolve.
var ExportsOrder = []string{
	"apiresourceschema-tenants.yaml",
	"apiresourceschema-projects.yaml",
	"apiresourceschema-memberships.yaml",
	"apiexport-platform.yaml",
	"apiexport-tenancy.yaml",
	"apiexport-provisioner.yaml",
	"apiexport-access.yaml",
	"rbac-bind.yaml",
	"workspacetypes.yaml",
	"apiexportendpointslices.yaml",
}

// StoreOrder is applied into the store workspace: the registry of Tenant
// records. It binds the Tenant API and nothing else.
var StoreOrder = []string{
	"store-bindings.yaml",
}

// TenantsOrder is applied into the workspace every tenant workspace is
// created under. It binds the capability to create them and nothing else.
//
// These two are the only tiers bound by hand, because nothing provisions
// them. Keeping them apart means a bug in the provisioning path cannot
// reach the registry that drives it, and the registry cannot create a
// workspace.
var TenantsOrder = []string{
	"tenants-bindings.yaml",
}
