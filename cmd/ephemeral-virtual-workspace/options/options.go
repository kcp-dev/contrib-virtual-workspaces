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

// Package options holds the command line surface of the ephemeral virtual
// workspace server.
package options

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	genericapiserveroptions "k8s.io/apiserver/pkg/server/options"
	"k8s.io/component-base/logs"
	logsapiv1 "k8s.io/component-base/logs/api/v1"

	frameworkoptions "github.com/kcp-dev/virtual-workspace-framework/pkg/options"

	"github.com/kcp-dev/contrib-virtual-ephemeral-resources-virtual-workspace/pkg/virtualworkspace/builder"
)

// DefaultRootPathPrefix matches kcp's, and must, because the URL this server
// answers on is written into APIExportEndpointSlice status by a kcp controller.
const DefaultRootPathPrefix = "/services"

// Options is the full command line surface.
type Options struct {
	// KubeconfigFile points at the kcp shard this virtual workspace is attached
	// to.
	KubeconfigFile string

	// CacheKubeconfigFile points at the kcp cache server. APIExports and
	// APIResourceSchemas owned by other shards are only visible there. When
	// empty, the shard kubeconfig is used, which is correct only for
	// single-shard deployments.
	CacheKubeconfigFile string

	// Context selects a context inside the kubeconfig files.
	Context string

	// ConfigFile is the registry of webhook-backed resources.
	ConfigFile string

	RootPathPrefix       string
	VirtualWorkspaceName string

	// AllowPrivateWebhookAddresses disables the guard that refuses webhook
	// endpoints resolving to loopback, link-local, or private addresses. Only
	// turn this on for local development.
	AllowPrivateWebhookAddresses bool

	// RequireExportContentAuthorization additionally checks that the caller
	// holds create on the APIExport's content subresource.
	RequireExportContentAuthorization bool

	SecureServing  genericapiserveroptions.SecureServingOptions
	Authentication genericapiserveroptions.DelegatingAuthenticationOptions
	Authorization  frameworkoptions.Authorization
	Audit          genericapiserveroptions.AuditOptions

	Logs *logs.Options

	ProfilerAddress string
}

// NewOptions returns the default options.
func NewOptions() *Options {
	opts := &Options{
		RootPathPrefix:       DefaultRootPathPrefix,
		VirtualWorkspaceName: builder.DefaultVirtualWorkspaceName,

		SecureServing:  *genericapiserveroptions.NewSecureServingOptions(),
		Authentication: *genericapiserveroptions.NewDelegatingAuthenticationOptions(),
		Authorization:  *frameworkoptions.NewAuthorization(),
		Audit:          *genericapiserveroptions.NewAuditOptions(),
		Logs:           logs.NewOptions(),

		// Both default to the safe end: refuse to reach cluster-internal
		// addresses, and require the same APIExport content permission the
		// replication virtual workspace requires.
		AllowPrivateWebhookAddresses:      false,
		RequireExportContentAuthorization: true,
	}

	opts.SecureServing.ServerCert.CertKey.CertFile = filepath.Join(".", ".kcp", "apiserver.crt")
	opts.SecureServing.ServerCert.CertKey.KeyFile = filepath.Join(".", ".kcp", "apiserver.key")
	opts.SecureServing.BindPort = 6454
	opts.Authentication.SkipInClusterLookup = true

	return opts
}

// AddFlags registers every flag.
func (o *Options) AddFlags(flags *pflag.FlagSet) {
	o.SecureServing.AddFlags(flags)
	o.Authentication.AddFlags(flags)
	o.Authorization.AddFlags(flags)
	o.Audit.AddFlags(flags)
	logsapiv1.AddFlags(o.Logs, flags)

	flags.StringVar(&o.KubeconfigFile, "kubeconfig", o.KubeconfigFile,
		"Kubeconfig of the kcp shard this virtual workspace is attached to.")
	_ = cobra.MarkFlagRequired(flags, "kubeconfig")

	flags.StringVar(&o.CacheKubeconfigFile, "cache-kubeconfig", o.CacheKubeconfigFile,
		"Kubeconfig of the kcp cache server. Required in sharded deployments, where the "+
			"APIExport being served may be owned by a different shard than this one. "+
			"Defaults to --kubeconfig.")

	flags.StringVar(&o.Context, "context", o.Context,
		"Name of the context to use in the kubeconfig files.")

	flags.StringVar(&o.ConfigFile, "ephemeral-config", o.ConfigFile,
		"YAML file registering the webhook-backed resources this server serves.")
	_ = cobra.MarkFlagRequired(flags, "ephemeral-config")

	flags.StringVar(&o.RootPathPrefix, "root-path-prefix", o.RootPathPrefix,
		"Prefix all virtual workspaces are served under.")

	flags.StringVar(&o.VirtualWorkspaceName, "virtual-workspace-name", o.VirtualWorkspaceName,
		"Path segment this virtual workspace is served under, giving "+
			"<external-url>/services/<name>/<cluster>/<export>. Name it after the service it "+
			"provides, e.g. ephemeral-buckets. The URL published into endpoint slices follows "+
			"this, so the only requirement is that nothing else on the same host answers there.")

	flags.BoolVar(&o.AllowPrivateWebhookAddresses, "allow-private-webhook-addresses", o.AllowPrivateWebhookAddresses,
		"Allow webhook endpoints that resolve to loopback, link-local or private addresses. "+
			"Development only: it lets a webhook read cluster-internal endpoints through this server.")

	flags.BoolVar(&o.RequireExportContentAuthorization, "require-export-content-authorization", o.RequireExportContentAuthorization,
		"Require the caller to be authorized for create on the APIExport's content subresource, "+
			"as the replication virtual workspace does.")

	flags.StringVar(&o.ProfilerAddress, "profiler-address", o.ProfilerAddress,
		"[Address]:port to bind the profiler to.")
}

// Validate checks the options.
func (o *Options) Validate() error {
	var errs []error

	errs = append(errs, o.SecureServing.Validate()...)
	errs = append(errs, o.Authentication.Validate()...)
	errs = append(errs, o.Authorization.Validate()...)

	if o.KubeconfigFile == "" {
		errs = append(errs, fmt.Errorf("--kubeconfig is required"))
	}
	if o.ConfigFile == "" {
		errs = append(errs, fmt.Errorf("--ephemeral-config is required"))
	}
	if !strings.HasPrefix(o.RootPathPrefix, "/") {
		errs = append(errs, fmt.Errorf("--root-path-prefix %q must start with /", o.RootPathPrefix))
	}
	if o.VirtualWorkspaceName == "" || strings.Contains(o.VirtualWorkspaceName, "/") {
		errs = append(errs, fmt.Errorf("--virtual-workspace-name %q must be a single non-empty path segment", o.VirtualWorkspaceName))
	}

	return utilerrors.NewAggregate(errs)
}
