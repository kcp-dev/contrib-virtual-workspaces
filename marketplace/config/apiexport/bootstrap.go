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

// Package apiexport holds the kcp-side assets the marketplace component
// needs.
//
// The model is three APIExports across three tiers, one of which declares
// no resources at all — an export with no schemas is the "grant a
// capability, add no API" primitive:
//
//   - marketplace-catalog      Providers. Bound into the catalog workspace.
//   - marketplace-publish      no resources; the claims the publish flow
//     needs in a provider workspace. Binding it IS a provider's consent.
//   - marketplace-provisioner  no resources; the claim to create provider
//     workspaces. Bound into the providers parent only.
//
// Enabling an offering involves NO marketplace machinery in the consumer
// workspace: publishing materializes a `bind` grant on the provider's own
// export, and a consumer enables by creating a plain kcp APIBinding there
// themselves — governed by their workspace's RBAC and kcp's bind check,
// nothing else.
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
	// path; APIBinding references and limitAllowedParents want paths.
	ExportsPathPlaceholder = "__EXPORTS_PATH__"
)

// ExportsOrder is the sequence the exports workspace's assets are applied
// in. It is load-bearing:
//
//   - schemas before the exports that name them, or the export is rejected
//     (the files are generated — `make generate` — and their kcp-style
//     content-addressed names are what the export manifests reference);
//   - the bind RBAC before the endpoint slices, because kcp gates slice
//     creation on a `bind` verb the creator does not otherwise hold;
//   - the workspace types after the exports they default-bind, so the
//     references resolve.
var ExportsOrder = []string{
	"apiresourceschema-providers.marketplace.platform.kcp.io.yaml",
	"apiexport-catalog.yaml",
	"apiexport-publish.yaml",
	"apiexport-provisioner.yaml",
	"rbac-bind.yaml",
	"workspacetypes.yaml",
	"apiexportendpointslices.yaml",
}

// CatalogOrder is applied into the catalog workspace: the registry of
// Provider records. It binds the Provider API and nothing else.
var CatalogOrder = []string{
	"catalog-bindings.yaml",
}

// ProvidersOrder is applied into the workspace provisioned provider
// workspaces are created under. It binds the capability to create them and
// nothing else.
//
// These two are the only tiers bound by hand, because nothing provisions
// them. Keeping them apart means a bug in the provisioning path cannot
// rewrite the records that drive it, and the record tier cannot create a
// workspace.
var ProvidersOrder = []string{
	"providers-bindings.yaml",
}
