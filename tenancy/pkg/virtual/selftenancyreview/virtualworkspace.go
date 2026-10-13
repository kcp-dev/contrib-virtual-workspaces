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

package selftenancyreview

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
	"github.com/kcp-dev/contrib-virtual-workspaces/tenancy/pkg/directory"
	"github.com/kcp-dev/contrib-virtual-workspaces/tenancy/pkg/virtual/membership"
	"github.com/kcp-dev/contrib-virtual-workspaces/tenancy/pkg/virtual/selfprojectrequest"
	"github.com/kcp-dev/contrib-virtual-workspaces/tenancy/pkg/virtual/selftenantrequest"
	tenancyv1alpha1 "github.com/kcp-dev/contrib-virtual-workspaces/tenancy/sdk/apis/tenancy/v1alpha1"
)

const VirtualWorkspaceName = "tenancy"

// RootPath is the URL prefix the front-proxy routes to this VW. The review
// is served at RootPath + /apis/tenancy.platform.kcp.io/v1alpha1/selftenancyreviews.
const RootPath = "/services/" + VirtualWorkspaceName

// Config is what the server supplies: the shared directory every answer
// is read from, how to render a workspace's URL, and the write paths —
// each one a function, because how this server reaches another workspace
// is a property of the deployment rather than of the API.
type Config struct {
	Directory   *directory.Directory
	EndpointFor func(cluster string) string

	CreateProject selfprojectrequest.CreateProjectFunc

	Tenants      selftenantrequest.Options
	CreateTenant selftenantrequest.CreateTenantFunc

	Memberships membership.Options
}

// NewVirtualWorkspace builds the tenancy VW: a fixed-group-version
// delegated apiserver serving tenancy.platform.kcp.io/v1alpha1, answered
// from the shared directory.
//
// The authorizer only requires an authenticated caller. That is not a gap:
// every resource here decides for itself what the caller may see or do,
// from the caller's own Memberships as the directory reports them. A
// coarser authorizer would have to be told about tenants, which is exactly
// the knowledge the directory already holds.
func NewVirtualWorkspace(cfg Config) rootapiserver.NamedVirtualWorkspace {
	d := cfg.Directory
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
				return errors.New("tenancy directory has not completed its initial sync")
			}
			return nil
		}),
		GroupVersionAPISets: []fixedgvs.GroupVersionAPISet{
			{
				GroupVersion: tenancyv1alpha1.SchemeGroupVersion,
				AddToScheme:  tenancyv1alpha1.AddToScheme,
				BootstrapRestResources: func(_ genericapiserver.CompletedConfig) (map[string]fixedgvs.RestStorageBuilder, error) {
					return map[string]fixedgvs.RestStorageBuilder{
						"selftenancyreviews": func(_ genericapiserver.CompletedConfig) (restStorage.Storage, error) {
							return NewREST(d, cfg.EndpointFor), nil
						},
						// Creating a project in a tenant the caller
						// administers. Authorized against the caller's own
						// Membership, performed with this server's
						// credential, because the tenant tier is
						// deliberately closed to its own members.
						"selfprojectrequests": func(_ genericapiserver.CompletedConfig) (restStorage.Storage, error) {
							return selfprojectrequest.NewREST(d, cfg.CreateProject), nil
						},
						// How someone with no membership at all gets
						// started: there is nothing to authorize against,
						// so the deployment decides whether this is
						// offered.
						"selftenantrequests": func(_ genericapiserver.CompletedConfig) (restStorage.Storage, error) {
							return selftenantrequest.NewREST(cfg.Tenants, cfg.CreateTenant), nil
						},
						// Managing who else may reach what. Mediated for
						// the same reason projects are: the grants live in
						// the workspace their holders may not enter.
						"memberships": func(_ genericapiserver.CompletedConfig) (restStorage.Storage, error) {
							return membership.NewREST(d, cfg.Memberships), nil
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
