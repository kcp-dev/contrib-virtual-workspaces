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

// Package server wires the tenancy virtual workspace binary together: the
// directory, the provider that fills it from organization workspaces, and
// a virtual-workspace root apiserver serving SelfTenancyReview at
// /services/tenancy behind kcp's front-proxy.
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
	"github.com/kcp-dev/contrib-virtual-workspaces/tenancy/pkg/directory"
	generatedopenapi "github.com/kcp-dev/contrib-virtual-workspaces/tenancy/pkg/generated/openapi"
	"github.com/kcp-dev/contrib-virtual-workspaces/tenancy/pkg/provider"
	"github.com/kcp-dev/contrib-virtual-workspaces/tenancy/pkg/virtual/selftenancyreview"
	tenancyv1alpha1 "github.com/kcp-dev/contrib-virtual-workspaces/tenancy/sdk/apis/tenancy/v1alpha1"
)

const debugDirectoryPath = "/debug/directory"

func retargetHost(host, workspacePath string) string {
	if i := strings.Index(host, "/clusters/"); i >= 0 {
		host = host[:i]
	}
	return strings.TrimSuffix(host, "/") + "/clusters/" + workspacePath
}

// endpointFor renders a workspace cluster into the URL callers reach it at.
func endpointFor(base string) func(cluster string) string {
	return func(cluster string) string {
		b := base
		if b != "" && !strings.HasSuffix(b, "/") {
			b += "/"
		}
		return b + cluster
	}
}

// Run starts the directory provider and serves the tenancy virtual
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
			RestConfig:    restConfig,
			PlatformSlice: o.PlatformSlice,
			TenancySlice:  o.TenancySlice,
		}, dir)
	}()

	klog.InfoS("tenancy directory provider running",
		"platformSlice", o.PlatformSlice, "tenancySlice", o.TenancySlice,
		"kubeconfig", o.Kubeconfig, "host", restConfig.Host)

	vws := []rootapiserver.NamedVirtualWorkspace{
		selftenancyreview.NewVirtualWorkspace(dir, endpointFor(o.EndpointBase)),
	}

	scheme := runtime.NewScheme()
	utilruntime.Must(tenancyv1alpha1.AddToScheme(scheme))
	codecs := serializer.NewCodecFactory(scheme)

	recommended := genericapiserver.NewRecommendedConfig(codecs)

	namer := openapinamer.NewDefinitionNamer(scheme)
	recommended.OpenAPIConfig = genericapiserver.DefaultOpenAPIConfig(generatedopenapi.GetOpenAPIDefinitions, namer)
	recommended.OpenAPIConfig.Info.Title = "kcp-tenancy-vw"
	recommended.OpenAPIV3Config = genericapiserver.DefaultOpenAPIV3Config(generatedopenapi.GetOpenAPIDefinitions, namer)
	recommended.OpenAPIV3Config.Info.Title = "kcp-tenancy-vw"

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

	// Debug endpoint: object counts, enough to see whether the directory
	// is being fed without exposing its contents to any authenticated
	// caller.
	rootServer.GenericAPIServer.Handler.NonGoRestfulMux.HandleFunc(debugDirectoryPath, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		tenants, projects, memberships := dir.Counts()
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{
			"ready":       dir.Ready(),
			"tenants":     tenants,
			"projects":    projects,
			"memberships": memberships,
		}); err != nil {
			klog.ErrorS(err, "encoding directory counts")
		}
	})

	prepared := rootServer.GenericAPIServer.PrepareRun()

	klog.InfoS("tenancy-vw serving",
		"selftenancyreview", selftenancyreview.RootPath+"/apis/"+tenancyv1alpha1.SchemeGroupVersion.Group+"/"+tenancyv1alpha1.SchemeGroupVersion.Version+"/selftenancyreviews",
	)

	serveErr := make(chan error, 1)
	go func() {
		serveErr <- prepared.RunWithContext(ctx)
	}()

	select {
	case err := <-providerErr:
		if err != nil && !errors.Is(err, context.Canceled) {
			return fmt.Errorf("directory provider failed: %w", err)
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
