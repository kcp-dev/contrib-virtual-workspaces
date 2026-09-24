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
	"github.com/kcp-dev/contrib-virtual-workspaces/marketplace/pkg/bootstrap"
)

// Options configures the marketplace virtual workspace server.
type Options struct {
	// SecureServing configures TLS serving.
	SecureServing *genericoptions.SecureServingOptions

	// Authentication identifies callers the same way the other virtual
	// workspaces do — JWT, forwarded request-header identity, client
	// certificates.
	Authentication *accessserver.Authentication

	// Authorization is the virtual-workspace-framework authorizer setup.
	Authorization *vwoptions.Authorization

	// Kubeconfig is the path to the kubeconfig for the target kcp.
	Kubeconfig string

	// CatalogSlice and PublishSlice feed the directory.
	CatalogSlice string
	PublishSlice string

	// WorkspacePath retargets the kubeconfig to the workspace holding the
	// endpoint slices. Empty means use the kubeconfig as-is.
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

	o.SecureServing.BindPort = 9446
	o.SecureServing.ServerCert.PairName = "marketplace-vw"

	return o
}

// AddFlags registers all flags on the given flag set.
func (o *Options) AddFlags(fs *pflag.FlagSet) {
	o.SecureServing.AddFlags(fs)
	o.Authentication.AddFlags(fs)
	o.Authorization.AddFlags(fs)

	fs.StringVar(&o.CatalogSlice, "catalog-endpointslice", bootstrap.ExportCatalog,
		"APIExportEndpointSlice serving Providers.")
	fs.StringVar(&o.PublishSlice, "publish-endpointslice", bootstrap.ExportPublish,
		"APIExportEndpointSlice serving Offerings.")
	fs.StringVar(&o.WorkspacePath, "workspace-path", "",
		"Workspace path the kubeconfig is retargeted to, e.g. root:marketplace:controllers. "+
			"Must be the workspace containing the APIExportEndpointSlices. "+
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

	if !o.Authentication.OIDCEnabled() && !o.Authentication.RequestHeaderEnabled() && !o.Authentication.ClientCertEnabled() {
		return fmt.Errorf("no authentication method configured: set --client-ca-file or " +
			"--authentication-config / --oidc-issuer-url for direct callers, and/or " +
			"--requestheader-client-ca-file when running behind kcp's front-proxy")
	}

	return nil
}

// Validate checks flag consistency, reporting every problem it finds.
func (o *Options) Validate() error {
	errs := []error{}
	errs = append(errs, o.SecureServing.Validate()...)
	errs = append(errs, o.Authentication.Validate()...)
	errs = append(errs, o.Authorization.Validate()...)

	if o.CatalogSlice == "" {
		errs = append(errs, fmt.Errorf("--catalog-endpointslice must not be empty"))
	}
	if o.PublishSlice == "" {
		errs = append(errs, fmt.Errorf("--publish-endpointslice must not be empty"))
	}
	if o.WorkspacePath != "" {
		if p := logicalcluster.NewPath(o.WorkspacePath); !p.IsValid() {
			errs = append(errs, fmt.Errorf("--workspace-path %q is not a valid workspace path", o.WorkspacePath))
		}
	}

	return errors.Join(errs...)
}
