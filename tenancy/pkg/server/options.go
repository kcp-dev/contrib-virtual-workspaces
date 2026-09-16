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

package server

import (
	"errors"
	"fmt"

	"github.com/spf13/pflag"

	genericoptions "k8s.io/apiserver/pkg/server/options"

	"github.com/kcp-dev/logicalcluster/v3"
	vwoptions "github.com/kcp-dev/virtual-workspace-framework/pkg/options"

	accessserver "github.com/kcp-dev/contrib-virtual-workspaces/access/pkg/server"
)

// Options configures the tenancy virtual workspace server.
type Options struct {
	// SecureServing configures TLS serving. The VW must serve TLS: behind
	// kcp's front-proxy the proxy verifies the VW's serving cert, and the
	// VW verifies the proxy's client cert via the requestheader CA.
	SecureServing *genericoptions.SecureServingOptions

	// Authentication identifies callers the same way the access VW does —
	// JWT, forwarded request-header identity, client certificates. The
	// configuration must match kcp's: the directory compares usernames and
	// groups verbatim against Membership subjects.
	Authentication *accessserver.Authentication

	// Authorization is the virtual-workspace-framework authorizer setup.
	Authorization *vwoptions.Authorization

	// Kubeconfig is the path to the kubeconfig for the target kcp, used by
	// the directory provider.
	Kubeconfig string

	// EndpointBase is the front-proxy URL prefix used to construct
	// workspace endpoints in review answers.
	EndpointBase string

	// APIExportEndpointSlice is the endpoint slice of the tenancy
	// APIExport; the directory provider follows it to find organization
	// workspaces.
	APIExportEndpointSlice string

	// WorkspacePath retargets the kubeconfig to the workspace holding the
	// endpoint slice, for kubeconfigs that point elsewhere (for example an
	// operator-minted admin kubeconfig pointing at root). Empty means use
	// the kubeconfig as-is.
	WorkspacePath string
}

// NewOptions returns options with defaults suitable for running behind
// kcp's front-proxy.
func NewOptions() *Options {
	o := &Options{
		SecureServing:  genericoptions.NewSecureServingOptions(),
		Authentication: accessserver.NewAuthentication(),
		Authorization:  vwoptions.NewAuthorization(),
	}

	o.SecureServing.BindPort = 9445
	o.SecureServing.ServerCert.PairName = "tenancy-vw"

	return o
}

// AddFlags registers all flags on the given flag set.
func (o *Options) AddFlags(fs *pflag.FlagSet) {
	o.SecureServing.AddFlags(fs)
	o.Authentication.AddFlags(fs)
	o.Authorization.AddFlags(fs)

	fs.StringVar(&o.EndpointBase, "endpoint-base", "https://kcp.example.com/clusters/",
		"FrontProxy URL prefix for workspace endpoints returned in review answers.")
	fs.StringVar(&o.APIExportEndpointSlice, "apiexport-endpointslice", "tenancy.contrib.kcp.io",
		"Name of the APIExportEndpointSlice for the tenancy APIExport; organization "+
			"workspaces are discovered through it.")
	fs.StringVar(&o.WorkspacePath, "workspace-path", "",
		"Workspace path the kubeconfig is retargeted to, e.g. root:tenancy:controllers. "+
			"Must be the workspace containing the APIExportEndpointSlice. "+
			"Empty means the kubeconfig's own cluster URL is used unchanged.")
	if fs.Lookup("kubeconfig") == nil {
		fs.StringVar(&o.Kubeconfig, "kubeconfig", "", "Path to the kubeconfig for the target kcp (required).")
	}
}

// Complete fills in derived defaults.
func (o *Options) Complete() error {
	if o.Kubeconfig == "" {
		return fmt.Errorf("--kubeconfig is required")
	}

	if !o.Authentication.OIDCEnabled() && !o.Authentication.RequestHeaderEnabled() {
		return fmt.Errorf("no authentication method configured: set --authentication-config or --oidc-issuer-url " +
			"for direct callers, and/or --requestheader-client-ca-file when running behind kcp's front-proxy")
	}

	return nil
}

// Validate checks flag consistency, reporting every problem it finds
// rather than only the first.
func (o *Options) Validate() error {
	errs := []error{}
	errs = append(errs, o.SecureServing.Validate()...)
	errs = append(errs, o.Authentication.Validate()...)
	errs = append(errs, o.Authorization.Validate()...)

	if o.APIExportEndpointSlice == "" {
		errs = append(errs, fmt.Errorf("--apiexport-endpointslice must not be empty"))
	}
	if o.WorkspacePath != "" {
		if p := logicalcluster.NewPath(o.WorkspacePath); !p.IsValid() {
			errs = append(errs, fmt.Errorf("--workspace-path %q is not a valid workspace path", o.WorkspacePath))
		}
	}

	return errors.Join(errs...)
}
