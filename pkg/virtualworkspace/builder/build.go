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

// Package builder assembles the ephemeral virtual workspace.
package builder

import (
	"context"
	"errors"
	"strings"

	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"
	kcpinformers "github.com/kcp-dev/sdk/client/informers/externalversions"
	"github.com/kcp-dev/virtual-workspace-framework/framework"
	virtualworkspacesdynamic "github.com/kcp-dev/virtual-workspace-framework/pkg/dynamic"
	"github.com/kcp-dev/virtual-workspace-framework/pkg/dynamic/apidefinition"
	dynamiccontext "github.com/kcp-dev/virtual-workspace-framework/pkg/dynamic/context"
	"github.com/kcp-dev/virtual-workspace-framework/pkg/rootapiserver"

	"github.com/kcp-dev/contrib-virtual-ephemeral-resources-virtual-workspace/pkg/config"
	"github.com/kcp-dev/contrib-virtual-ephemeral-resources-virtual-workspace/pkg/virtualworkspace/apidefinitions"
	"github.com/kcp-dev/contrib-virtual-ephemeral-resources-virtual-workspace/pkg/virtualworkspace/apidomainkey"
	ephemeralauthorizer "github.com/kcp-dev/contrib-virtual-ephemeral-resources-virtual-workspace/pkg/virtualworkspace/authorizer"
	"github.com/kcp-dev/contrib-virtual-ephemeral-resources-virtual-workspace/pkg/webhook"
)

// DefaultVirtualWorkspaceName is the path segment this virtual workspace is
// mounted under, giving URLs of the shape
//
//	<external URL>/services/ephemeral/<cluster>/<export>
//
// Name it after the service it provides -- --virtual-workspace-name=ephemeral-buckets
// serves /services/ephemeral-buckets/... -- because the name is only ever seen
// by whoever operates it. The URL a shard uses is whatever this server
// publishes into the endpoint slice an APIExport references, so any name works
// as long as the two agree, which pkg/endpointslice takes care of.
//
// It used to default to "apiexport", squatting on the path kcp's own APIExport
// virtual workspace answers on. That was not a choice: the URL came from kcp's
// controller as <shard.spec.virtualWorkspaceURL>/services/apiexport/..., so
// this server had to answer there, and an operator had to route around the
// collision. Publishing the URL removes the collision along with the need to
// route by API group.
const DefaultVirtualWorkspaceName = "ephemeral"

// Config is everything BuildVirtualWorkspace needs.
type Config struct {
	// Configuration is the operator's registry of webhook-backed resources.
	Configuration *config.Configuration

	// WebhookFactory builds the HTTP clients used to call providers.
	WebhookFactory *webhook.Factory

	// RootPathPrefix is the prefix all virtual workspaces are served under,
	// conventionally "/services".
	RootPathPrefix string

	// VirtualWorkspaceName is the path segment identifying this virtual
	// workspace. Defaults to DefaultVirtualWorkspaceName.
	VirtualWorkspaceName string

	// AuthorizerOptions tune the content authorizer.
	AuthorizerOptions ephemeralauthorizer.Options

	KubeClusterClient kcpkubernetesclientset.ClusterInterface

	// LocalInformers watch the shard this virtual workspace is attached to;
	// CacheInformers watch the cache server, which is where APIExports and
	// APIResourceSchemas owned by other shards are found.
	LocalInformers kcpinformers.SharedInformerFactory
	CacheInformers kcpinformers.SharedInformerFactory
}

// BuildVirtualWorkspace assembles the ephemeral virtual workspace.
func BuildVirtualWorkspace(cfg Config) ([]rootapiserver.NamedVirtualWorkspace, error) {
	name := cfg.VirtualWorkspaceName
	if name == "" {
		name = DefaultVirtualWorkspaceName
	}

	rootPathPrefix := cfg.RootPathPrefix
	if !strings.HasSuffix(rootPathPrefix, "/") {
		rootPathPrefix += "/"
	}
	rootPathPrefix += name + "/"

	readyCh := make(chan struct{})

	getter, err := apidefinitions.NewGetter(
		cfg.Configuration,
		cfg.WebhookFactory,
		cfg.LocalInformers.Apis().V1alpha2().APIExports().Lister(),
		cfg.CacheInformers.Apis().V1alpha2().APIExports().Lister(),
		cfg.LocalInformers.Apis().V1alpha1().APIResourceSchemas().Lister(),
		cfg.CacheInformers.Apis().V1alpha1().APIResourceSchemas().Lister(),
	)
	if err != nil {
		return nil, err
	}

	contentAuthorizer := ephemeralauthorizer.New(
		cfg.AuthorizerOptions,
		getter,
		cfg.KubeClusterClient,
		cfg.LocalInformers.Apis().V1alpha2().APIExports().Lister(),
		cfg.CacheInformers.Apis().V1alpha2().APIExports().Lister(),
		cfg.LocalInformers.Apis().V1alpha2().APIBindings().Lister(),
	)

	vw := &virtualworkspacesdynamic.DynamicVirtualWorkspace{
		RootPathResolver: framework.RootPathResolverFunc(func(urlPath string, ctx context.Context) (bool, string, context.Context) {
			cluster, domainKey, prefixToStrip, ok := digestURL(urlPath, rootPathPrefix)
			if !ok {
				return false, "", ctx
			}

			completed := genericapirequest.WithCluster(ctx, cluster)
			completed = dynamiccontext.WithAPIDomainKey(completed, domainKey)

			return true, prefixToStrip, completed
		}),

		ReadyChecker: framework.ReadyFunc(func() error {
			select {
			case <-readyCh:
				return nil
			default:
				return errors.New("ephemeral virtual workspace informers are not synced yet")
			}
		}),

		Authorizer: contentAuthorizer,

		BootstrapAPISetManagement: func(mainConfig genericapiserver.CompletedConfig) (apidefinition.APIDefinitionSetGetter, error) {
			getter.SetServerConfig(mainConfig)

			// Calling Informer() registers the informer with the factory. This
			// has to happen before the factory is started in the command, so it
			// is done here rather than in the post-start hook.
			informers := map[string]cache.SharedIndexInformer{
				"local/apiexports":         cfg.LocalInformers.Apis().V1alpha2().APIExports().Informer(),
				"local/apibindings":        cfg.LocalInformers.Apis().V1alpha2().APIBindings().Informer(),
				"local/apiresourceschemas": cfg.LocalInformers.Apis().V1alpha1().APIResourceSchemas().Informer(),
				"cache/apiexports":         cfg.CacheInformers.Apis().V1alpha2().APIExports().Informer(),
				"cache/apiresourceschemas": cfg.CacheInformers.Apis().V1alpha1().APIResourceSchemas().Informer(),
			}

			if err := mainConfig.AddPostStartHook("ephemeral-virtual-workspace", func(hookContext genericapiserver.PostStartHookContext) error {
				defer close(readyCh)

				for name, informer := range informers {
					if !cache.WaitForNamedCacheSync(name, hookContext.Done(), informer.HasSynced) {
						klog.Background().Error(nil, "informer not synced", "informer", name)
						return nil
					}
				}

				return nil
			}); err != nil {
				return nil, err
			}

			return getter, nil
		},
	}

	return []rootapiserver.NamedVirtualWorkspace{{Name: name, VirtualWorkspace: vw}}, nil
}

// digestURL parses
//
//	/services/apiexport/<export-cluster>/<export-name>/clusters/<target>/apis/<group>/<version>/<resource>
//
// into the target cluster, the API domain key identifying the export, and the
// prefix the framework has to strip before handing the request to the
// delegated API server.
func digestURL(urlPath, rootPathPrefix string) (
	cluster genericapirequest.Cluster,
	domainKey dynamiccontext.APIDomainKey,
	prefixToStrip string,
	accepted bool,
) {
	if !strings.HasPrefix(urlPath, rootPathPrefix) {
		return genericapirequest.Cluster{}, "", "", false
	}

	withoutPrefix := strings.TrimPrefix(urlPath, rootPathPrefix)

	parts := strings.SplitN(withoutPrefix, "/", 3)
	if len(parts) < 3 {
		return genericapirequest.Cluster{}, "", "", false
	}

	exportCluster, exportName := parts[0], parts[1]
	if exportCluster == "" || exportName == "" {
		return genericapirequest.Cluster{}, "", "", false
	}

	realPath := "/" + parts[2]
	if !strings.HasPrefix(realPath, "/clusters/") {
		return genericapirequest.Cluster{}, "", "", false
	}

	withoutClusters := strings.TrimPrefix(realPath, "/clusters/")
	parts = strings.SplitN(withoutClusters, "/", 2)
	path := logicalcluster.NewPath(parts[0])

	realPath = "/"
	if len(parts) > 1 {
		realPath += parts[1]
	}

	if path == logicalcluster.Wildcard {
		cluster.Wildcard = true
	} else {
		var ok bool
		cluster.Name, ok = path.Name()
		if !ok {
			return genericapirequest.Cluster{}, "", "", false
		}
	}

	return cluster,
		apidomainkey.New(logicalcluster.Name(exportCluster), exportName),
		strings.TrimSuffix(urlPath, realPath),
		true
}
