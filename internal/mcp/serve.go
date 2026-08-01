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
	"net"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	genericapiserver "k8s.io/apiserver/pkg/server"
	genericoptions "k8s.io/apiserver/pkg/server/options"
	kubeoptions "k8s.io/kubernetes/pkg/kubeapiserver/options"
	"k8s.io/client-go/rest"
	netutils "k8s.io/utils/net"

	"github.com/kcp-dev/virtual-workspace-framework/pkg/rootapiserver"

	"github.com/kcp-dev/contrib-mcp-virtual-workspace/internal/access"
)

// ServeOptions configures the HTTP surface.
type ServeOptions struct {
	SecureServing  *genericoptions.SecureServingOptions
	Authentication *kubeoptions.BuiltInAuthenticationOptions

	// Access resolves which workspaces a caller may use.
	Access *access.Client

	// Impersonator is the base credential for per-workspace requests.
	Impersonator *rest.Config
}

// Serve runs the MCP virtual workspace until ctx is cancelled.
//
// The shape follows the framework's root API server even though MCP is not a
// Kubernetes API: registering as a raw-handler virtual workspace means the
// caller is authenticated, authorized and audited by the same filter chain as
// every other kcp endpoint, rather than by hand-rolled middleware.
func Serve(ctx context.Context, opts ServeOptions) error {
	// An empty scheme is enough: no Kubernetes types are served, but the
	// generic apiserver still needs a codec factory to build.
	codecs := serializer.NewCodecFactory(runtime.NewScheme())
	recommended := genericapiserver.NewRecommendedConfig(codecs)

	if err := opts.SecureServing.MaybeDefaultWithSelfSignedCerts("localhost", nil,
		[]net.IP{netutils.ParseIPSloppy("127.0.0.1")}); err != nil {
		return fmt.Errorf("defaulting serving certificates: %w", err)
	}
	if err := opts.SecureServing.ApplyTo(&recommended.Config.SecureServing); err != nil {
		return fmt.Errorf("configuring secure serving: %w", err)
	}
	if err := applyAuthentication(ctx, opts.Authentication, &recommended.Config); err != nil {
		return err
	}

	vw := NewVirtualWorkspace(opts.Access, opts.Impersonator)
	vws := []rootapiserver.NamedVirtualWorkspace{vw}

	rootCfg, err := rootapiserver.NewConfig(recommended)
	if err != nil {
		return fmt.Errorf("building root apiserver config: %w", err)
	}
	rootCfg.Extra.VirtualWorkspaces = vws

	if err := applyAuthorization(&recommended.Config, vws); err != nil {
		return err
	}

	server, err := rootapiserver.NewServer(rootCfg.Complete(), genericapiserver.NewEmptyDelegate())
	if err != nil {
		return fmt.Errorf("building root apiserver: %w", err)
	}

	return server.GenericAPIServer.PrepareRun().RunWithContext(ctx)
}
