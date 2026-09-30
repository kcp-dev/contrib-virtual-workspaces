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

package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/kcp-dev/contrib-virtual-workspaces/marketplace/pkg/bootstrap"
	"github.com/kcp-dev/contrib-virtual-workspaces/marketplace/pkg/operator"
)

func newOperatorCommand() *cobra.Command {
	var (
		kubeconfig    string
		workspacePath string
		catalogPath   string
		providersPath string
		bindUsers     []string
		bindGroups    []string
	)

	cmd := &cobra.Command{
		Use:   "operator",
		Short: "Reconcile Providers into workspaces, and published APIExports into bind grants",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if kubeconfig == "" {
				return fmt.Errorf("--kubeconfig is required")
			}

			cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
			if err != nil {
				return fmt.Errorf("load kubeconfig: %w", err)
			}
			if workspacePath == "" {
				return fmt.Errorf("--workspace-path is required: it is where the exports and WorkspaceTypes live")
			}
			cfg.Host = retargetHost(cfg.Host, workspacePath)

			ctx := genericapiserver.SetupSignalContext()
			return operator.Run(ctx, operator.Options{
				RestConfig:    cfg,
				ExportsPath:   workspacePath,
				CatalogPath:   catalogPath,
				ProvidersPath: providersPath,
				BindUsers:     bindUsers,
				BindGroups:    bindGroups,
			})
		},
	}

	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "Path to the kubeconfig for the target kcp (required).")
	cmd.Flags().StringVar(&workspacePath, "workspace-path",
		bootstrap.DefaultWorkspacePrefix+":"+bootstrap.DefaultControllersWorkspace,
		"Workspace holding the marketplace APIExports, their endpoint slices and the "+
			"provider/consumer WorkspaceTypes.")
	cmd.Flags().StringVar(&catalogPath, "catalog-workspace",
		bootstrap.DefaultWorkspacePrefix+":"+bootstrap.DefaultCatalogWorkspace,
		"Workspace Provider records live in.")
	cmd.Flags().StringVar(&providersPath, "providers-workspace",
		bootstrap.DefaultWorkspacePrefix+":"+bootstrap.DefaultProvidersWorkspace,
		"Workspace provisioned provider workspaces are created under. Deliberately not "+
			"the workspace Provider records live in.")
	cmd.Flags().StringSliceVar(&bindUsers, "bind-user", nil,
		"User the per-offering `bind` grants name: who may enable a published offering. Repeatable.")
	cmd.Flags().StringSliceVar(&bindGroups, "bind-group", nil,
		"Group the per-offering `bind` grants name. Repeatable. Both flags empty means "+
			"system:authenticated — any authenticated user may enable in a workspace their "+
			"own RBAC lets them bind in.")

	return cmd
}
