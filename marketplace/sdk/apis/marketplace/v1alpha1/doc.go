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

// Package v1alpha1 holds the API types for the marketplace component
// (group marketplace.platform.kcp.io).
//
// One persisted, cluster-scoped kind: Provider, the registration record,
// in the catalog workspace. One virtual kind: CatalogOffering, served
// read-only by the marketplace virtual workspace — the catalog as users
// browse it.
//
// There is deliberately no kind for an offering and none for an
// enablement, because neither needs one. Publishing is annotating an
// APIExport (see AnnotationPublish), so the offering is the export.
// Enabling is creating a plain kcp APIBinding, so the enablement is the
// binding. Both are ordinary objects their own owner controls, which is
// why the marketplace has no write path into either workspace.
//
// +k8s:openapi-gen=true
// +groupName=marketplace.platform.kcp.io
package v1alpha1
