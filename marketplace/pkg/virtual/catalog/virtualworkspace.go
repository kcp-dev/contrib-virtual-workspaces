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

// Package catalog implements the marketplace virtual workspace: the
// read-only catalog at /services/marketplace. Each entry carries the bind
// coordinates — export, workspace path, claims at the current revision —
// so browsing the catalog is reading both the consent form and the
// APIBinding a consumer authors to enable. Enabling itself involves no
// virtual workspace at all: it is a plain kcp bind.
package catalog

import (
	"context"
	"errors"
	"strings"

	restStorage "k8s.io/apiserver/pkg/registry/rest"
	genericapiserver "k8s.io/apiserver/pkg/server"

	"github.com/kcp-dev/virtual-workspace-framework/framework"
	"github.com/kcp-dev/virtual-workspace-framework/pkg/fixedgvs"
	"github.com/kcp-dev/virtual-workspace-framework/pkg/rootapiserver"

	"github.com/kcp-dev/contrib-virtual-workspaces/access/pkg/virtual"
	"github.com/kcp-dev/contrib-virtual-workspaces/marketplace/pkg/directory"
	marketplacev1alpha1 "github.com/kcp-dev/contrib-virtual-workspaces/marketplace/sdk/apis/marketplace/v1alpha1"
)

const VirtualWorkspaceName = "marketplace"

// RootPath is the URL prefix the front-proxy routes to this VW.
//
//	GET  RootPath/apis/marketplace.contrib.kcp.io/v1alpha1/offerings
const RootPath = "/services/" + VirtualWorkspaceName

// NewVirtualWorkspace builds the marketplace VW: a fixed-group-version
// delegated apiserver serving marketplace.contrib.kcp.io/v1alpha1 with the
// read-only catalog, answered from the shared directory. Any authenticated
// caller may browse.
func NewVirtualWorkspace(d *directory.Directory) rootapiserver.NamedVirtualWorkspace {
	vw := &fixedgvs.FixedGroupVersionsVirtualWorkspace{
		RootPathResolver: framework.RootPathResolverFunc(func(urlPath string, ctx context.Context) (bool, string, context.Context) {
			if urlPath != RootPath && !strings.HasPrefix(urlPath, RootPath+"/") {
				return false, "", ctx
			}
			return true, RootPath, ctx
		}),
		Authorizer: virtual.AuthenticatedOnlyAuthorizer(),
		ReadyChecker: framework.ReadyFunc(func() error {
			if !d.Ready() {
				return errors.New("marketplace directory has not completed its initial sync")
			}
			return nil
		}),
		GroupVersionAPISets: []fixedgvs.GroupVersionAPISet{
			{
				GroupVersion: marketplacev1alpha1.SchemeGroupVersion,
				AddToScheme:  marketplacev1alpha1.AddToScheme,
				BootstrapRestResources: func(_ genericapiserver.CompletedConfig) (map[string]fixedgvs.RestStorageBuilder, error) {
					return map[string]fixedgvs.RestStorageBuilder{
						"offerings": func(_ genericapiserver.CompletedConfig) (restStorage.Storage, error) {
							return NewOfferingsREST(d), nil
						},
					}, nil
				},
			},
		},
	}

	return rootapiserver.NamedVirtualWorkspace{
		Name:             VirtualWorkspaceName,
		VirtualWorkspace: &virtual.WithoutAdmission{CoreVirtualWorkspace: vw},
	}
}
