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

package config

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/pflag"

	"github.com/kcp-dev/contrib-mcp-virtual-workspace/pkg/toolsets"

	genericoptions "k8s.io/apiserver/pkg/server/options"
	kubeoptions "k8s.io/kubernetes/pkg/kubeapiserver/options"
)

// AccessConfig points at the access virtual workspace, which answers "which
// workspaces can this caller see".
//
// This component deliberately holds no access graph of its own. Duplicating the
// RBAC indexer would double the watch load on kcp and give two answers that can
// disagree; asking the access VW makes it the single source of truth and makes
// SelfClusterAccessReview earn its keep as a real API rather than an internal
// function call.
type AccessConfig struct {
	// URL is the base address of the access virtual workspace, either through
	// kcp's front-proxy (https://<front-proxy>/services/access) or directly at
	// the service.
	URL string

	// CAFile verifies the access VW's serving certificate. Empty uses the
	// system pool.
	CAFile string

	// CacheTTL bounds how long a caller's workspace list is reused.
	//
	// Without it every MCP tool call costs a round trip to the access VW. With
	// it, a revoked binding stays visible for up to this long — the access graph
	// is already eventually consistent, so this adds to an existing window
	// rather than introducing a new class of staleness.
	CacheTTL time.Duration
}

// KcpConfig is how this component reaches the workspaces a caller is allowed to
// use.
//
// The credential is the server's own ServiceAccount, not the caller's: behind
// the front-proxy the caller's bearer token is consumed by the proxy and never
// arrives here. Per-workspace calls therefore impersonate the caller, which
// means this identity needs impersonate on users, groups and
// userextras.authentication.k8s.io.
type KcpConfig struct {
	// Kubeconfig is the server's own credential, used as the base for
	// impersonated per-workspace requests.
	Kubeconfig string
}

// ServerConfig is the full flag surface.
type ServerConfig struct {
	Access AccessConfig
	Kcp    KcpConfig

	// Toolsets names the tool groups this server exposes. The selection is
	// enforced here, server-side, so it is policy rather than a client
	// preference: a toolset that is not enabled never exists for any caller.
	Toolsets []string

	// SecureServing is TLS-only on purpose: behind the front-proxy the proxy
	// verifies this server's certificate, and this server verifies the proxy's
	// via the requestheader CA.
	SecureServing *genericoptions.SecureServingOptions

	// Authentication must resolve callers to the same usernames kcp does.
	// Identity is only used to ask the access VW and to impersonate, both of
	// which compare names verbatim.
	Authentication *kubeoptions.BuiltInAuthenticationOptions

	// shardExternalURL and cacheKubeconfig are accepted and ignored. kcp's
	// own virtual-workspace server takes them, so deployments templated for
	// that server pass them through.
	shardExternalURL string
	cacheKubeconfig  string
}

// NewServerConfig returns the defaults for running behind kcp's front-proxy.
func NewServerConfig() ServerConfig {
	secure := genericoptions.NewSecureServingOptions()
	secure.BindPort = 9444
	secure.ServerCert.PairName = "mcp-virtual-workspace"

	return ServerConfig{
		Access: AccessConfig{
			CacheTTL: 30 * time.Second,
		},
		Toolsets:      toolsets.Default(),
		SecureServing: secure,
		Authentication: kubeoptions.NewBuiltInAuthenticationOptions().
			WithAnonymous().
			WithClientCert().
			WithOIDC().
			WithRequestHeader(),
	}
}

// AddFlags registers every flag on fs.
func (c *ServerConfig) AddFlags(fs *pflag.FlagSet) {
	c.SecureServing.AddFlags(fs)
	c.Authentication.AddFlags(fs)

	fs.StringVar(&c.Access.URL, "access-url", c.Access.URL,
		"Base URL of the access virtual workspace, e.g. "+
			"https://front-proxy.kcp-system.svc:6443/services/access.")
	fs.StringVar(&c.Access.CAFile, "access-ca-file", c.Access.CAFile,
		"CA bundle verifying the access virtual workspace's serving certificate.")
	fs.DurationVar(&c.Access.CacheTTL, "access-cache-ttl", c.Access.CacheTTL,
		"How long a caller's workspace list is reused before asking the access "+
			"virtual workspace again.")
	fs.StringVar(&c.Kcp.Kubeconfig, "kubeconfig", c.Kcp.Kubeconfig,
		"Kubeconfig for this server's own identity, used to impersonate callers "+
			"on per-workspace requests.")
	fs.StringVar(&c.shardExternalURL, "shard-external-url", "",
		"Accepted for compatibility with kcp's own virtual-workspace server and ignored. "+
			"Workspaces come from --access-url.")
	fs.StringVar(&c.cacheKubeconfig, "cache-kubeconfig", "",
		"Accepted for compatibility with kcp's own virtual-workspace server and ignored. "+
			"All kcp reads go through --kubeconfig.")
	fs.StringSliceVar(&c.Toolsets, "toolsets", c.Toolsets,
		"Comma-separated toolsets to expose. Valid toolsets: "+
			strings.Join(toolsets.Names(), ", ")+".")
}

// Validate reports configuration that cannot work.
func (c *ServerConfig) Validate() error {
	if c.Access.URL == "" {
		return fmt.Errorf("--access-url is required")
	}
	if c.Kcp.Kubeconfig == "" {
		return fmt.Errorf("--kubeconfig is required")
	}
	if len(c.Toolsets) == 0 {
		return fmt.Errorf("--toolsets must name at least one toolset; valid toolsets are %s",
			strings.Join(toolsets.Names(), ", "))
	}
	if err := toolsets.Validate(c.Toolsets); err != nil {
		return err
	}

	oidc := c.Authentication.AuthenticationConfigFile != "" ||
		(c.Authentication.OIDC != nil && c.Authentication.OIDC.IssuerURL != "")
	requestHeader := c.Authentication.RequestHeader != nil &&
		c.Authentication.RequestHeader.ClientCAFile != ""
	if !oidc && !requestHeader {
		return fmt.Errorf("no authentication method configured: set --authentication-config " +
			"or --oidc-issuer-url for direct callers, and/or --requestheader-client-ca-file " +
			"when running behind kcp's front-proxy")
	}

	return nil
}
