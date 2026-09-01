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

// Command endpointslice-controller publishes a virtual workspace's address into
// the EphemeralResourceEndpointSlices of one workspace.
//
// It is a separate process from the virtual workspace, and runs in the
// workspace the APIExport lives in -- typically alongside whatever else a
// provider runs for that workspace. The two jobs have nothing in common: the
// virtual workspace serves requests for every workspace that binds an export,
// while this writes a URL into one provider's objects, and only needs
// credentials for that one workspace.
package main

import (
	"context"
	goflag "flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/pflag"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"

	ephemeralv1alpha1 "github.com/kcp-dev/contrib-virtual-workspaces/ephemeral/pkg/apis/ephemeral/v1alpha1"
	"github.com/kcp-dev/contrib-virtual-workspaces/ephemeral/pkg/endpointslice"
)

func main() {
	var (
		kubeconfig  = pflag.String("kubeconfig", "", "Kubeconfig of the workspace the APIExport lives in, e.g. one produced by `kubectl ws use :root:providers:s3`. Its current context decides which workspace is published into.")
		contextName = pflag.String("context", "", "Name of the context to use in the kubeconfig.")

		externalURL = pflag.String("external-url", "", "HTTPS URL kcp should use to reach the virtual workspace, without a path, e.g. https://ephemeral.example.com:6454.")
		vwName      = pflag.String("virtual-workspace-name", "ephemeral", "Path segment the virtual workspace is served under. Must match how it was started.")
		prefix      = pflag.String("root-path-prefix", "/services", "Prefix all virtual workspaces are served under. Must match how it was started.")

		exportName  = pflag.String("export", "", "Only publish into slices for this APIExport. Empty means every slice in the workspace.")
		shardLabels = pflag.StringToString("shards", nil, "Labels selecting the shards that should use this URL, e.g. region=eu, published as shards.selector. Empty publishes shards.matchAll, which is every shard and what one virtual workspace serving the whole installation wants.")

		resync = pflag.Duration("resync-interval", 10*time.Second, "How often to reconcile.")
	)

	klogFlags := goflag.NewFlagSet("klog", goflag.ContinueOnError)
	klog.InitFlags(klogFlags)
	pflag.CommandLine.AddGoFlagSet(klogFlags)
	pflag.Parse()

	ctx := signalContext()
	logger := klog.FromContext(ctx).WithValues("component", "endpointslice-controller")
	ctx = klog.NewContext(ctx, logger)

	if err := run(ctx, runOptions{
		kubeconfig:  *kubeconfig,
		contextName: *contextName,
		controller: endpointslice.Options{
			ExternalURL:          *externalURL,
			RootPathPrefix:       *prefix,
			VirtualWorkspaceName: *vwName,
			ExportName:           *exportName,
			Shards:               shardSelector(*shardLabels),
			ResyncInterval:       *resync,
		},
	}); err != nil {
		logger.Error(err, "exiting")
		os.Exit(1)
	}
}

type runOptions struct {
	kubeconfig  string
	contextName string
	controller  endpointslice.Options
}

func run(ctx context.Context, o runOptions) error {
	cfg, err := restConfig(o.kubeconfig, o.contextName)
	if err != nil {
		return fmt.Errorf("loading --kubeconfig: %w", err)
	}

	// A plain, workspace-scoped client: the kubeconfig's server URL already
	// carries /clusters/<workspace>, so there is no cluster-aware client and no
	// wildcard access involved.
	client, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("building dynamic client: %w", err)
	}

	controller, err := endpointslice.NewController(client, o.controller)
	if err != nil {
		return err
	}

	klog.FromContext(ctx).Info("Starting", "workspace", cfg.Host)
	controller.Start(ctx)

	return nil
}

// shardSelector turns --shards into the selector to publish.
//
// No labels means matchAll -- every shard -- rather than an empty label
// selector. The two are different in the API on purpose: matchAll is the whole
// installation, a selector is a subset of it, and saying neither would ask kcp
// to match the URL by prefix against the shard's own address, which is exactly
// what publishing avoids.
func shardSelector(labels map[string]string) ephemeralv1alpha1.EndpointSelector {
	if len(labels) == 0 {
		return ephemeralv1alpha1.EndpointSelector{MatchAll: true}
	}
	return ephemeralv1alpha1.EndpointSelector{
		Selector: &metav1.LabelSelector{MatchLabels: labels},
	}
}

func restConfig(kubeconfig, contextName string) (*rest.Config, error) {
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	loadingRules.ExplicitPath = kubeconfig

	startingConfig, err := loadingRules.GetStartingConfig()
	if err != nil {
		return nil, err
	}

	return clientcmd.NewDefaultClientConfig(*startingConfig, &clientcmd.ConfigOverrides{
		CurrentContext: contextName,
	}).ClientConfig()
}

func signalContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan os.Signal, 2)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-ch
		cancel()
		<-ch
		os.Exit(1) // a second signal means now
	}()
	return ctx
}
