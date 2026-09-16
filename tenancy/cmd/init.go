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
	"context"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"

	accessbootstrap "github.com/kcp-dev/contrib-virtual-workspaces/access/pkg/bootstrap"
	"github.com/kcp-dev/contrib-virtual-workspaces/tenancy/pkg/bootstrap"
)

func newInitCommand() *cobra.Command {
	var (
		kubeconfig           string
		workspacePrefix      string
		controllersWorkspace string
		workspaceType        string
		serverUsers          []string
		serverGroups         []string
		timeout              time.Duration
	)

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Install the tenancy APIExport, schemas and endpoint slice into kcp",
		Long: `Install the kcp-side objects the tenancy component needs — the
APIResourceSchemas, the APIExport, the bind RBAC and the endpoint slice —
and wait until the slice publishes virtual workspace URLs.

The objects land in <prefix>:<controllers-workspace>, default
root:tenancy:controllers. Any missing workspace along that path is
created, so the credential needs rights to create workspaces from the
root down. Idempotent: safe to run on every pod start and every upgrade.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
			defer cancel()
			logger := klog.FromContext(ctx)

			if kubeconfig == "" {
				kubeconfig = os.Getenv("KUBECONFIG")
			}
			if kubeconfig == "" {
				return fmt.Errorf("--kubeconfig is required, or set KUBECONFIG")
			}

			cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
			if err != nil {
				return fmt.Errorf("load kubeconfig: %w", err)
			}

			workspacePath, err := accessbootstrap.JoinWorkspacePath(workspacePrefix, controllersWorkspace)
			if err != nil {
				return err
			}

			target, err := accessbootstrap.CreateWorkspacePath(ctx, cfg, workspacePath, workspaceType)
			if err != nil {
				return fmt.Errorf("resolve workspace path %s: %w", workspacePath, err)
			}

			result, err := bootstrap.Bootstrap(ctx, target, bootstrap.Options{
				ServerUsers:  serverUsers,
				ServerGroups: serverGroups,
			})
			if err != nil {
				return err
			}

			logger.Info("bootstrap complete",
				"workspace", workspacePath,
				"apiExportEndpointSlice", result.APIExportEndpointSlice,
				"virtualWorkspaceURLs", result.VirtualWorkspaceURLs,
			)
			logger.Info("organization workspaces opt in with an APIBinding to this export; "+
				"see config/examples/apibinding-consumer.yaml",
				"exportPath", workspacePath,
			)
			return nil
		},
	}

	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "",
		"Path to the kubeconfig for the target kcp. Defaults to $KUBECONFIG.")
	cmd.Flags().StringVar(&workspacePrefix, "workspace-prefix", bootstrap.DefaultWorkspacePrefix,
		"Parent path the controllers workspace is created under.")
	cmd.Flags().StringVar(&controllersWorkspace, "controllers-workspace", bootstrap.DefaultControllersWorkspace,
		"Name of the workspace this component owns, created under --workspace-prefix. "+
			"Holds the APIExport and the endpoint slice.")
	cmd.Flags().StringVar(&workspaceType, "workspace-type", accessbootstrap.DefaultWorkspaceType,
		"WorkspaceType for any workspace this creates.")
	cmd.Flags().StringSliceVar(&serverUsers, "server-user", nil,
		"Additional User to grant the server role to, repeatable. Needed when the operator "+
			"or the virtual workspace does not run as this bootstrap's own credential.")
	cmd.Flags().StringSliceVar(&serverGroups, "server-group", nil,
		"Additional Group to grant the server role to, repeatable.")
	cmd.Flags().DurationVar(&timeout, "timeout", 3*time.Minute,
		"Overall time budget for installing and verifying.")

	return cmd
}
