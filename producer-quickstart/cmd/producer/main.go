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

// The producer's controller binary.
//
// It runs against the export's virtual workspace, not against any one
// workspace: the kubeconfig points at the producer's own workspace, where
// the APIExportEndpointSlice lives, and the multicluster provider follows
// that slice to every workspace that enabled the export. Each becomes a
// cluster the manager knows, and the controller reconciles Databases in all
// of them with one credential — the producer's own.
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	ctrlconfig "sigs.k8s.io/controller-runtime/pkg/client/config"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"

	"github.com/kcp-dev/multicluster-provider/apiexport"
	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"

	dbaasv1alpha1 "example.com/dbaas-producer/apis/v1alpha1"
	"example.com/dbaas-producer/internal/controller"
)

func main() {
	var (
		workspacePath string
		slice         string
		metricsAddr   string
	)
	// --kubeconfig is controller-runtime's: registered by its config
	// package on the default flag set, falling back to $KUBECONFIG and then
	// to in-cluster config, so it is not declared here.
	flag.StringVar(&workspacePath, "workspace-path", "",
		"The producer's workspace, e.g. root:marketplace:producers:dbaas: where the "+
			"export and its endpoint slice are. Empty uses the kubeconfig's cluster URL as is.")
	flag.StringVar(&slice, "apiexport-endpointslice", "dbaas",
		"APIExportEndpointSlice to follow. The clusters on it are the workspaces that enabled the export.")
	flag.StringVar(&metricsAddr, "metrics-bind-address", "0",
		"Address for Prometheus metrics; 0 disables them.")
	zapOpts := zap.Options{}
	zapOpts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&zapOpts)))
	logger := ctrl.Log.WithName("dbaas-producer")

	if err := run(workspacePath, slice, metricsAddr); err != nil {
		logger.Error(err, "exiting")
		os.Exit(1)
	}
}

func run(workspacePath, slice, metricsAddr string) error {
	cfg, err := ctrlconfig.GetConfig()
	if err != nil {
		return fmt.Errorf("load kubeconfig: %w", err)
	}
	if workspacePath != "" {
		cfg.Host = retargetHost(cfg.Host, workspacePath)
	}

	// The scheme needs the producer's own kinds, the kcp kinds the provider
	// watches to discover workspaces (APIBindings), and core.
	sch := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(sch))
	utilruntime.Must(apisv1alpha1.AddToScheme(sch))
	utilruntime.Must(dbaasv1alpha1.AddToScheme(sch))

	providerLog := ctrl.Log.WithName("provider")
	provider, err := apiexport.New(cfg, slice, apiexport.Options{Scheme: sch, Log: &providerLog})
	if err != nil {
		return fmt.Errorf("construct provider for slice %q: %w", slice, err)
	}
	mgr, err := mcmanager.New(cfg, provider, manager.Options{
		Scheme:  sch,
		Metrics: metricsserver.Options{BindAddress: metricsAddr},
	})
	if err != nil {
		return fmt.Errorf("construct manager: %w", err)
	}
	if err := (&controller.Reconciler{}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("set up database controller: %w", err)
	}

	ctrl.Log.Info("dbaas producer running", "host", cfg.Host, "slice", slice)
	return mgr.Start(ctrl.SetupSignalHandler())
}

// retargetHost points a kubeconfig's server URL at a workspace by path,
// whatever cluster it named before.
func retargetHost(host, workspacePath string) string {
	if i := strings.Index(host, "/clusters/"); i >= 0 {
		host = host[:i]
	}
	return strings.TrimSuffix(host, "/") + "/clusters/" + workspacePath
}
