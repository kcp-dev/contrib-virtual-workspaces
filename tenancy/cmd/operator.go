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

	"github.com/kcp-dev/contrib-virtual-workspaces/tenancy/pkg/bootstrap"
	"github.com/kcp-dev/contrib-virtual-workspaces/tenancy/pkg/naming"
	"github.com/kcp-dev/contrib-virtual-workspaces/tenancy/pkg/operator"
)

func newOperatorCommand() *cobra.Command {
	var (
		kubeconfig     string
		workspacePath  string
		tenantsPath    string
		namingStrategy string
	)

	cmd := &cobra.Command{
		Use:   "operator",
		Short: "Reconcile Tenants, Projects and Memberships into workspaces and RBAC",
		RunE: func(cmd *cobra.Command, _ []string) error {
			strategy, err := naming.ForName(namingStrategy)
			if err != nil {
				return err
			}
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
				RestConfig:  cfg,
				Strategy:    strategy,
				ExportsPath: workspacePath,
				TenantsPath: tenantsPath,
			})
		},
	}

	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "Path to the kubeconfig for the target kcp (required).")
	cmd.Flags().StringVar(&workspacePath, "workspace-path",
		bootstrap.DefaultWorkspacePrefix+":"+bootstrap.DefaultControllersWorkspace,
		"Workspace holding the tenancy APIExports, their endpoint slices and the "+
			"tenant/project WorkspaceTypes.")
	cmd.Flags().StringVar(&tenantsPath, "tenants-workspace",
		bootstrap.DefaultWorkspacePrefix+":"+bootstrap.DefaultTenantsWorkspace,
		"Workspace every tenant workspace is created under. Deliberately not the "+
			"workspace Tenant records live in.")
	cmd.Flags().StringVar(&namingStrategy, "naming-strategy", naming.StrategySlug,
		fmt.Sprintf("How workspaces are named after tenants and projects: %q slugifies the "+
			"display name and falls back to a UID suffix on collision, %q always names by UID.",
			naming.StrategySlug, naming.StrategyUID))

	return cmd
}
