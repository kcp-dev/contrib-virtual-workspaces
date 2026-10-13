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

// Package server wires the marketplace virtual workspace binary together:
// the directory, the producer that fills it, and a virtual-workspace root
// apiserver serving the catalog read-only at /services/marketplace behind
// kcp's front-proxy.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	"k8s.io/apiserver/pkg/authorization/union"
	openapinamer "k8s.io/apiserver/pkg/endpoints/openapi"
	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"
	netutils "k8s.io/utils/net"

	"github.com/kcp-dev/virtual-workspace-framework/pkg/rootapiserver"

	"github.com/kcp-dev/contrib-virtual-workspaces/access/pkg/virtual"
	"github.com/kcp-dev/contrib-virtual-workspaces/marketplace/pkg/directory"
	generatedopenapi "github.com/kcp-dev/contrib-virtual-workspaces/marketplace/pkg/generated/openapi"
	"github.com/kcp-dev/contrib-virtual-workspaces/marketplace/pkg/provider"
	"github.com/kcp-dev/contrib-virtual-workspaces/marketplace/pkg/virtual/catalog"
	marketplacev1alpha1 "github.com/kcp-dev/contrib-virtual-workspaces/marketplace/sdk/apis/marketplace/v1alpha1"
)

const debugDirectoryPath = "/debug/directory"

func retargetHost(host, workspacePath string) string {
	if i := strings.Index(host, "/clusters/"); i >= 0 {
		host = host[:i]
	}
	return strings.TrimSuffix(host, "/") + "/clusters/" + workspacePath
}

// Run starts the directory producer and serves the marketplace virtual
// workspace until ctx is cancelled.
func Run(ctx context.Context, o *Options) error {
	if err := o.Complete(); err != nil {
		return err
	}
	if err := o.Validate(); err != nil {
		return err
	}

	restConfig, err := clientcmd.BuildConfigFromFlags("", o.Kubeconfig)
	if err != nil {
		return fmt.Errorf("load kubeconfig: %w", err)
	}
	if o.WorkspacePath != "" {
		restConfig.Host = retargetHost(restConfig.Host, o.WorkspacePath)
	}

	dir := directory.New()

	providerErr := make(chan error, 1)
	go func() {
		providerErr <- provider.Run(ctx, provider.Options{
			RestConfig:   restConfig,
			CatalogSlice: o.CatalogSlice,
			PublishSlice: o.PublishSlice,
		}, dir)
	}()

	klog.InfoS("marketplace directory producer running",
		"catalogSlice", o.CatalogSlice, "publishSlice", o.PublishSlice,
		"kubeconfig", o.Kubeconfig, "host", restConfig.Host)

	vws := []rootapiserver.NamedVirtualWorkspace{
		catalog.NewVirtualWorkspace(dir),
	}

	scheme := runtime.NewScheme()
	utilruntime.Must(marketplacev1alpha1.AddToScheme(scheme))
	codecs := serializer.NewCodecFactory(scheme)

	recommended := genericapiserver.NewRecommendedConfig(codecs)

	namer := openapinamer.NewDefinitionNamer(scheme)
	recommended.OpenAPIConfig = genericapiserver.DefaultOpenAPIConfig(generatedopenapi.GetOpenAPIDefinitions, namer)
	recommended.OpenAPIConfig.Info.Title = "kcp-marketplace-vw"
	recommended.OpenAPIV3Config = genericapiserver.DefaultOpenAPIV3Config(generatedopenapi.GetOpenAPIDefinitions, namer)
	recommended.OpenAPIV3Config.Info.Title = "kcp-marketplace-vw"

	rootCfg, err := rootapiserver.NewConfig(recommended)
	if err != nil {
		return fmt.Errorf("create root apiserver config: %w", err)
	}
	rootCfg.Extra.VirtualWorkspaces = vws

	if err := o.SecureServing.MaybeDefaultWithSelfSignedCerts("localhost", nil, []net.IP{netutils.ParseIPSloppy("127.0.0.1")}); err != nil {
		return fmt.Errorf("default serving certs: %w", err)
	}
	if err := o.SecureServing.ApplyTo(&recommended.SecureServing); err != nil {
		return fmt.Errorf("apply secure serving: %w", err)
	}
	if err := o.Authentication.ApplyTo(ctx, &recommended.Authentication, recommended.SecureServing); err != nil {
		return fmt.Errorf("apply authentication: %w", err)
	}
	if err := o.Authorization.ApplyTo(&recommended.Config, func() []rootapiserver.NamedVirtualWorkspace { return vws }); err != nil {
		return fmt.Errorf("apply authorization: %w", err)
	}

	// The debug endpoint is outside every virtual workspace's root path, so
	// the framework's authorizer resolves it to no virtual workspace and
	// denies it. Granting it explicitly is what makes it reachable at all.
	recommended.Authorization.Authorizer = union.New(
		recommended.Authorization.Authorizer,
		pathScopedAuthorizer(debugDirectoryPath, virtual.AuthenticatedOnlyAuthorizer()),
	)

	completed := rootCfg.Complete()
	rootServer, err := rootapiserver.NewServer(completed, genericapiserver.NewEmptyDelegate())
	if err != nil {
		return fmt.Errorf("create root apiserver: %w", err)
	}

	rootServer.GenericAPIServer.Handler.NonGoRestfulMux.HandleFunc(debugDirectoryPath, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		producers, offerings := dir.Counts()
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{
			"ready":     dir.Ready(),
			"producers": producers,
			"offerings": offerings,
		}); err != nil {
			klog.ErrorS(err, "encoding directory counts")
		}
	})

	prepared := rootServer.GenericAPIServer.PrepareRun()

	klog.InfoS("marketplace-vw serving",
		"offerings", catalog.RootPath+"/apis/"+marketplacev1alpha1.SchemeGroupVersion.Group+"/"+marketplacev1alpha1.SchemeGroupVersion.Version+"/offerings",
	)

	serveErr := make(chan error, 1)
	go func() {
		serveErr <- prepared.RunWithContext(ctx)
	}()

	select {
	case err := <-providerErr:
		if err != nil && !errors.Is(err, context.Canceled) {
			return fmt.Errorf("directory producer failed: %w", err)
		}
		<-serveErr
		return nil
	case err := <-serveErr:
		return err
	}
}

// pathScopedAuthorizer delegates only for one non-resource path, and has no
// opinion on anything else.
func pathScopedAuthorizer(path string, delegate authorizer.Authorizer) authorizer.Authorizer {
	return authorizer.AuthorizerFunc(func(ctx context.Context, attrs authorizer.Attributes) (authorizer.Decision, string, error) {
		if !attrs.IsResourceRequest() && attrs.GetPath() == path {
			return delegate.Authorize(ctx, attrs)
		}
		return authorizer.DecisionNoOpinion, "", nil
	})
}
