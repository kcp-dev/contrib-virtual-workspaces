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

package mcp

import (
	"context"
	"fmt"

	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	"k8s.io/apiserver/pkg/authorization/authorizerfactory"
	"k8s.io/apiserver/pkg/authorization/path"
	"k8s.io/apiserver/pkg/authorization/union"
	genericapiserver "k8s.io/apiserver/pkg/server"
	kubeoptions "k8s.io/kubernetes/pkg/kubeapiserver/options"

	vwauthorization "github.com/kcp-dev/virtual-workspace-framework/pkg/authorization"
	"github.com/kcp-dev/virtual-workspace-framework/pkg/rootapiserver"
)

// applyAuthentication builds the union authenticator and advertises the client
// and requestheader CAs on the serving side.
func applyAuthentication(ctx context.Context, opts *kubeoptions.BuiltInAuthenticationOptions, cfg *genericapiserver.Config) error {
	authenticatorConfig, err := opts.ToAuthenticationConfig()
	if err != nil {
		return fmt.Errorf("building authentication config: %w", err)
	}

	if authenticatorConfig.ClientCAContentProvider != nil {
		if err := cfg.Authentication.ApplyClientCert(authenticatorConfig.ClientCAContentProvider, cfg.SecureServing); err != nil {
			return fmt.Errorf("loading client CA: %w", err)
		}
	}

	// Advertised in the TLS handshake, not only used for verification: without
	// this the front-proxy sends no client certificate and its identity headers
	// are dropped.
	if authenticatorConfig.RequestHeaderConfig != nil && authenticatorConfig.RequestHeaderConfig.CAContentProvider != nil {
		if err := cfg.Authentication.ApplyClientCert(authenticatorConfig.RequestHeaderConfig.CAContentProvider, cfg.SecureServing); err != nil {
			return fmt.Errorf("loading requestheader CA: %w", err)
		}
	}

	cfg.Authentication.APIAudiences = opts.APIAudiences
	cfg.Authentication.Authenticator, _, _, _, err = authenticatorConfig.New(ctx)
	if err != nil {
		return fmt.Errorf("building authenticator: %w", err)
	}

	return nil
}

func applyAuthorization(cfg *genericapiserver.Config, vws []rootapiserver.NamedVirtualWorkspace) error {
	healthz, err := path.NewAuthorizer([]string{"/healthz", "/readyz", "/livez"})
	if err != nil {
		return fmt.Errorf("building path authorizer: %w", err)
	}

	cfg.Authorization.Authorizer = union.New(
		authorizerfactory.NewPrivilegedGroups(user.SystemPrivilegedGroup),
		healthz,
		vwauthorization.NewVirtualWorkspaceAuthorizer(func() []rootapiserver.NamedVirtualWorkspace { return vws }),
	)

	return nil
}

// authenticatedOnly allows any authenticated, non-anonymous caller.
//
// There is nothing further to authorize at this layer: the workspaces a caller
// may reach come from the access virtual workspace, and kcp re-authorizes every
// per-workspace request as the impersonated user.
//
// Impersonation is the one exception. The generic server's WithImpersonation
// filter honors Impersonate-* headers whenever this authorizer allows the
// impersonate verb, so allowing it here would let any authenticated caller
// become any other user. This server impersonates on its outbound calls only;
// no inbound caller needs it.
func authenticatedOnly() authorizer.Authorizer {
	return authorizer.AuthorizerFunc(func(_ context.Context, attrs authorizer.Attributes) (authorizer.Decision, string, error) {
		if attrs.GetVerb() == "impersonate" {
			return authorizer.DecisionDeny, "impersonation is not supported", nil
		}
		u := attrs.GetUser()
		if u == nil || u.GetName() == "" || u.GetName() == user.Anonymous {
			return authorizer.DecisionDeny, "authentication required", nil
		}
		return authorizer.DecisionAllow, "available to any authenticated caller", nil
	})
}
