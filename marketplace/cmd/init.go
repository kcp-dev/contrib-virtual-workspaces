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
	"github.com/kcp-dev/contrib-virtual-workspaces/marketplace/pkg/bootstrap"
)

func newInitCommand() *cobra.Command {
	var (
		kubeconfig           string
		workspacePrefix      string
		controllersWorkspace string
		catalogWorkspace     string
		producersWorkspace   string
		workspaceType        string
		serverUsers          []string
		serverGroups         []string
		adminUsers           []string
		adminGroups          []string
		timeout              time.Duration
	)

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Install the marketplace APIExports, schemas and endpoint slices into kcp",
		Long: `Install the kcp-side objects the marketplace needs — the schemas, the
three APIExports, the bind RBAC, the producer WorkspaceType and the
endpoint slices — and bind the two hand-bound tiers: the catalog
(Provider records) and the producers parent (where provisioned producer
workspaces are created).

The objects land in <prefix>:<controllers-workspace>, default
root:marketplace:controllers. Any missing workspace along that path is
created. Idempotent: safe to run on every pod start and every upgrade.`,
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

			result, err := bootstrap.Bootstrap(ctx, target, workspacePath, bootstrap.Options{
				ServerUsers:  serverUsers,
				ServerGroups: serverGroups,
			})
			if err != nil {
				return err
			}

			// The two hand-bound tiers every install gets for free. They are
			// separate on purpose: the catalog holds the records, the
			// producers workspace is the parent producer workspaces are
			// created under, and neither can do the other's job.
			var catalogPath, producersPath string
			if catalogWorkspace != "" {
				catalogPath, err = accessbootstrap.JoinWorkspacePath(workspacePrefix, catalogWorkspace)
				if err != nil {
					return err
				}
				logger.Info("creating the catalog", "workspace", catalogPath)
				catalogCfg, err := accessbootstrap.CreateWorkspacePath(ctx, cfg, catalogPath, workspaceType)
				if err != nil {
					return fmt.Errorf("resolve workspace path %s: %w", catalogPath, err)
				}
				if err := bootstrap.BindCatalog(ctx, catalogCfg, result); err != nil {
					return fmt.Errorf("bind the Provider API into %s: %w", catalogPath, err)
				}
				if err := bootstrap.GrantPlatformAdmins(ctx, catalogCfg, adminUsers, adminGroups); err != nil {
					return fmt.Errorf("grant platform admins in %s: %w", catalogPath, err)
				}
				logger.Info("catalog ready", "workspace", catalogPath,
					"platformAdminUsers", adminUsers, "platformAdminGroups", adminGroups)
			}
			if producersWorkspace != "" {
				producersPath, err = accessbootstrap.JoinWorkspacePath(workspacePrefix, producersWorkspace)
				if err != nil {
					return err
				}
				logger.Info("creating the producers parent", "workspace", producersPath)
				providersCfg, err := accessbootstrap.CreateWorkspacePath(ctx, cfg, producersPath, workspaceType)
				if err != nil {
					return fmt.Errorf("resolve workspace path %s: %w", producersPath, err)
				}
				if err := bootstrap.BindProvidersParent(ctx, providersCfg, result); err != nil {
					return fmt.Errorf("bind the provisioner claim into %s: %w", producersPath, err)
				}
				logger.Info("producers parent ready", "workspace", producersPath)
			}

			// Only now is there a consumer, so only now can a slice have
			// URLs to report. Informational: the servers follow the slices
			// themselves and pick them up whenever they appear.
			sliceURLs := map[string][]string{}
			for _, slice := range []string{
				bootstrap.ExportCatalog, bootstrap.ExportPublish, bootstrap.ExportProvisioner,
			} {
				urls, err := bootstrap.WaitForEndpointSliceURLs(ctx, target, slice)
				if err != nil {
					return err
				}
				sliceURLs[slice] = urls
			}

			logger.Info("bootstrap complete",
				"workspace", workspacePath,
				"exportsCluster", result.ExportsCluster,
				"catalogUrls", sliceURLs[bootstrap.ExportCatalog],
				"publishUrls", sliceURLs[bootstrap.ExportPublish],
				"provisionerUrls", sliceURLs[bootstrap.ExportProvisioner],
			)
			if catalogPath != "" {
				logger.Info("register Providers here; a producer publishes by annotating an APIExport in its own workspace",
					"catalog", catalogPath, "provisionedUnder", producersPath,
				)
			}
			logger.Info("producer workspaces get marketplace-publish through the `producer` type, or bind "+
				"it by hand to register BYO; consumers need nothing bound - enabling an offering is a "+
				"plain kcp APIBinding they create themselves",
				"exportsPath", workspacePath,
				"workspaceTypes", []string{bootstrap.WorkspaceTypeProvider},
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
			"Holds the APIExports, the WorkspaceTypes and the endpoint slices.")
	cmd.Flags().StringVar(&catalogWorkspace, "catalog-workspace", bootstrap.DefaultCatalogWorkspace,
		"Name of the workspace Provider records live in, created under --workspace-prefix "+
			"and bound to the Provider API. Empty skips creating it.")
	cmd.Flags().StringVar(&producersWorkspace, "producers-workspace", bootstrap.DefaultProducersWorkspace,
		"Name of the workspace provisioned producer workspaces are created under, bound to "+
			"the provisioner claim. Holds no marketplace objects. Empty skips creating it.")
	cmd.Flags().StringVar(&workspaceType, "workspace-type", accessbootstrap.DefaultWorkspaceType,
		"WorkspaceType for any workspace this creates.")
	cmd.Flags().StringSliceVar(&serverUsers, "server-user", nil,
		"Additional User to grant the server role to, repeatable. Needed when the operator "+
			"or the virtual workspace does not run as this bootstrap's own credential.")
	cmd.Flags().StringSliceVar(&serverGroups, "server-group", nil,
		"Additional Group to grant the server role to, repeatable.")
	cmd.Flags().StringSliceVar(&adminUsers, "platform-admin-user", nil,
		"User who may manage Producers in the catalog workspace, repeatable. Bound to a role "+
			"that allows exactly that; anyone left off the list on a later run loses it.")
	cmd.Flags().StringSliceVar(&adminGroups, "platform-admin-group", nil,
		"Group, as the identity provider asserts it, whose members may manage Producers, repeatable.")
	cmd.Flags().DurationVar(&timeout, "timeout", 3*time.Minute,
		"Overall time budget for installing and verifying.")

	return cmd
}
