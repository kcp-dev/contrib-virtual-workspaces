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

// Package command wires the ephemeral virtual workspace server together.
package command

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/spf13/cobra"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/component-base/featuregate"
	logsapiv1 "k8s.io/component-base/logs/api/v1"
	"k8s.io/klog/v2"

	kcpkubernetesclient "github.com/kcp-dev/client-go/kubernetes"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	kcpinformers "github.com/kcp-dev/sdk/client/informers/externalversions"
	virtualrootapiserver "github.com/kcp-dev/virtual-workspace-framework/pkg/rootapiserver"

	"github.com/kcp-dev/contrib-virtual-ephemeral-resources-virtual-workspace/cmd/ephemeral-virtual-workspace/options"
	"github.com/kcp-dev/contrib-virtual-ephemeral-resources-virtual-workspace/pkg/cacheclient"
	"github.com/kcp-dev/contrib-virtual-ephemeral-resources-virtual-workspace/pkg/config"
	ephemeralauthorizer "github.com/kcp-dev/contrib-virtual-ephemeral-resources-virtual-workspace/pkg/virtualworkspace/authorizer"
	"github.com/kcp-dev/contrib-virtual-ephemeral-resources-virtual-workspace/pkg/virtualworkspace/builder"
	"github.com/kcp-dev/contrib-virtual-ephemeral-resources-virtual-workspace/pkg/webhook"
)

const informerResync = 10 * time.Minute

// NewCommand builds the root cobra command.
func NewCommand(ctx context.Context) *cobra.Command {
	opts := options.NewOptions()
	opts.Logs.Verbosity = logsapiv1.VerbosityLevel(2)

	cmd := &cobra.Command{
		Use:   "ephemeral-virtual-workspace",
		Short: "Serve webhook-backed, non-persisted resources for kcp APIExports",
		Long: "Serves resources whose instances are never written to etcd. A create is answered " +
			"synchronously by the provider's webhook and the answer is returned to the client. " +
			"Only the create verb is served.",

		RunE: func(*cobra.Command, []string) error {
			// The logging options consult feature gates of their own
			// (ContextualLogging and friends), and looking up a gate that was
			// never registered panics rather than defaulting.
			featureGate := featuregate.NewFeatureGate()
			if err := logsapiv1.AddFeatureGates(featureGate); err != nil {
				return err
			}
			if err := logsapiv1.ValidateAndApply(opts.Logs, featureGate); err != nil {
				return err
			}
			if err := opts.Validate(); err != nil {
				return err
			}
			return Run(ctx, opts)
		},
	}

	opts.AddFlags(cmd.Flags())

	return cmd
}

// Run starts the server and blocks until ctx is cancelled or serving fails.
func Run(ctx context.Context, o *options.Options) error {
	logger := klog.FromContext(ctx).WithValues("component", "ephemeral-virtual-workspace")

	ephemeralConfig, err := config.Load(o.ConfigFile)
	if err != nil {
		return err
	}
	logger.Info("Loaded ephemeral webhook configuration", "resources", len(ephemeralConfig.Resources))

	shardConfig, err := restConfig(o.KubeconfigFile, o.Context)
	if err != nil {
		return fmt.Errorf("loading --kubeconfig: %w", err)
	}
	// Informers must not be throttled.
	shardConfig.QPS = -1

	// The kcp clients add the /clusters/<name> prefix themselves, so any path
	// baked into the kubeconfig host has to go.
	if err := stripPath(shardConfig); err != nil {
		return err
	}

	cacheBase := shardConfig
	if o.CacheKubeconfigFile != "" {
		cacheBase, err = restConfig(o.CacheKubeconfigFile, o.Context)
		if err != nil {
			return fmt.Errorf("loading --cache-kubeconfig: %w", err)
		}
		cacheBase.QPS = -1
		if err := stripPath(cacheBase); err != nil {
			return err
		}
	}
	cacheConfig := cacheclient.RestConfig(cacheBase)

	kcpClusterClient, err := kcpclientset.NewForConfig(shardConfig)
	if err != nil {
		return fmt.Errorf("building kcp client: %w", err)
	}
	cacheKcpClusterClient, err := kcpclientset.NewForConfig(cacheConfig)
	if err != nil {
		return fmt.Errorf("building cache client: %w", err)
	}
	kubeClusterClient, err := kcpkubernetesclient.NewForConfig(shardConfig)
	if err != nil {
		return fmt.Errorf("building kube client: %w", err)
	}

	localInformers := kcpinformers.NewSharedInformerFactory(kcpClusterClient, informerResync)
	cacheInformers := kcpinformers.NewSharedInformerFactory(cacheKcpClusterClient, informerResync)

	if o.ProfilerAddress != "" {
		//nolint:errcheck,gosec
		go http.ListenAndServe(o.ProfilerAddress, nil)
	}

	// Assemble the generic API server.
	scheme := runtime.NewScheme()
	metav1.AddToGroupVersion(scheme, schema.GroupVersion{Group: "", Version: "v1"})
	codecs := serializer.NewCodecFactory(scheme)

	recommendedConfig := genericapiserver.NewRecommendedConfig(codecs)
	if err := o.SecureServing.ApplyTo(&recommendedConfig.Config.SecureServing); err != nil {
		return err
	}
	if err := o.Authentication.ApplyTo(&recommendedConfig.Authentication, recommendedConfig.SecureServing, recommendedConfig.OpenAPIConfig); err != nil {
		return err
	}
	if err := o.Audit.ApplyTo(&recommendedConfig.Config); err != nil {
		return err
	}

	rootAPIServerConfig, err := virtualrootapiserver.NewConfig(recommendedConfig)
	if err != nil {
		return err
	}

	if err := o.Authorization.ApplyTo(&recommendedConfig.Config, func() []virtualrootapiserver.NamedVirtualWorkspace {
		return rootAPIServerConfig.Extra.VirtualWorkspaces
	}); err != nil {
		return err
	}

	rootAPIServerConfig.Extra.VirtualWorkspaces, err = builder.BuildVirtualWorkspace(builder.Config{
		Configuration:        ephemeralConfig,
		WebhookFactory:       webhook.NewFactory(webhook.Options{AllowPrivateAddresses: o.AllowPrivateWebhookAddresses}),
		RootPathPrefix:       o.RootPathPrefix,
		VirtualWorkspaceName: o.VirtualWorkspaceName,
		AuthorizerOptions: ephemeralauthorizer.Options{
			RequireExportContent: o.RequireExportContentAuthorization,
		},
		KubeClusterClient: kubeClusterClient,
		LocalInformers:    localInformers,
		CacheInformers:    cacheInformers,
	})
	if err != nil {
		return err
	}

	completedConfig := rootAPIServerConfig.Complete()
	rootAPIServer, err := virtualrootapiserver.NewServer(completedConfig, genericapiserver.NewEmptyDelegate())
	if err != nil {
		return err
	}

	prepared := rootAPIServer.GenericAPIServer.PrepareRun()
	if err := completedConfig.WithOpenAPIAggregationController(prepared.GenericAPIServer); err != nil {
		return err
	}

	// Informers are registered by BuildVirtualWorkspace above, so starting them
	// here picks all of them up.
	logger.Info("Starting informers")
	localInformers.Start(ctx.Done())
	cacheInformers.Start(ctx.Done())
	localInformers.WaitForCacheSync(ctx.Done())
	cacheInformers.WaitForCacheSync(ctx.Done())

	logger.Info("Starting ephemeral virtual workspace apiserver",
		"externalAddress", rootAPIServerConfig.Generic.ExternalAddress,
		"path", fmt.Sprintf("%s/%s", o.RootPathPrefix, o.VirtualWorkspaceName))

	return prepared.RunWithContext(ctx)
}

func restConfig(kubeconfigFile, context string) (*rest.Config, error) {
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	loadingRules.ExplicitPath = kubeconfigFile

	startingConfig, err := loadingRules.GetStartingConfig()
	if err != nil {
		return nil, err
	}

	return clientcmd.NewDefaultClientConfig(*startingConfig, &clientcmd.ConfigOverrides{
		CurrentContext: context,
	}).ClientConfig()
}

func stripPath(cfg *rest.Config) error {
	u, err := url.Parse(cfg.Host)
	if err != nil {
		return fmt.Errorf("parsing host %q: %w", cfg.Host, err)
	}
	u.Path = ""
	cfg.Host = u.String()
	return nil
}
