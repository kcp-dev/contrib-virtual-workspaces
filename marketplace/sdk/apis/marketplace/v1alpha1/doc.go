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
// (group marketplace.contrib.kcp.io).
//
// Three persisted, cluster-scoped kinds, each living in a different tier:
// Provider records live in the catalog workspace, an Offering lives in the
// workspace of the provider that publishes it, and an Enablement lives in
// the consumer workspace that orders one. One virtual kind —
// CatalogOffering — is served read-only by the marketplace virtual
// workspace and is the catalog as users browse it.
//
// +k8s:openapi-gen=true
// +groupName=marketplace.contrib.kcp.io
package v1alpha1
