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
		storeWorkspace       string
		tenantsWorkspace     string
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

			result, err := bootstrap.Bootstrap(ctx, target, workspacePath, bootstrap.Options{
				ServerUsers:  serverUsers,
				ServerGroups: serverGroups,
			})
			if err != nil {
				return err
			}

			// The two hand-bound tiers every install gets for free. Without
			// them a fresh deployment has the API installed but nowhere to
			// put a Tenant, and binding by hand means copying an identity
			// hash that only this code knows.
			//
			// They are separate on purpose: the store holds the records,
			// the tenants workspace is the parent the workspaces are
			// created under, and neither can do the other's job.
			var storePath, tenantsPath string
			if storeWorkspace != "" {
				storePath, err = accessbootstrap.JoinWorkspacePath(workspacePrefix, storeWorkspace)
				if err != nil {
					return err
				}
				logger.Info("creating the tenant store", "workspace", storePath)
				storeCfg, err := accessbootstrap.CreateWorkspacePath(ctx, cfg, storePath, workspaceType)
				if err != nil {
					return fmt.Errorf("resolve workspace path %s: %w", storePath, err)
				}
				if err := bootstrap.BindStore(ctx, storeCfg, result); err != nil {
					return fmt.Errorf("bind the Tenant API into %s: %w", storePath, err)
				}
				logger.Info("tenant store ready", "workspace", storePath)
			}
			if tenantsWorkspace != "" {
				tenantsPath, err = accessbootstrap.JoinWorkspacePath(workspacePrefix, tenantsWorkspace)
				if err != nil {
					return err
				}
				logger.Info("creating the provisioning parent", "workspace", tenantsPath)
				tenantsCfg, err := accessbootstrap.CreateWorkspacePath(ctx, cfg, tenantsPath, workspaceType)
				if err != nil {
					return fmt.Errorf("resolve workspace path %s: %w", tenantsPath, err)
				}
				if err := bootstrap.BindTenantsParent(ctx, tenantsCfg, result); err != nil {
					return fmt.Errorf("bind the provisioner claim into %s: %w", tenantsPath, err)
				}
				logger.Info("provisioning parent ready", "workspace", tenantsPath)
			}

			// Only now is there a consumer, so only now can a slice have
			// URLs to report. Informational: the servers follow the slices
			// themselves and pick them up whenever they appear.
			sliceURLs := map[string][]string{}
			for _, slice := range []string{
				bootstrap.ExportPlatform, bootstrap.ExportTenancy,
				bootstrap.ExportProvisioner, bootstrap.ExportAccess,
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
				"platformUrls", sliceURLs[bootstrap.ExportPlatform],
				"tenancyUrls", sliceURLs[bootstrap.ExportTenancy],
				"provisionerUrls", sliceURLs[bootstrap.ExportProvisioner],
				"accessUrls", sliceURLs[bootstrap.ExportAccess],
			)
			if storePath != "" {
				logger.Info("put Tenants here; their Projects and Memberships live inside each tenant's own workspace",
					"store", storePath, "provisionedUnder", tenantsPath,
				)
			}
			logger.Info("tenant and project workspaces bind the capability exports through their WorkspaceType, "+
				"so nothing below this tier is bound by hand",
				"exportsPath", workspacePath,
				"workspaceTypes", []string{bootstrap.WorkspaceTypeTenant, bootstrap.WorkspaceTypeProject},
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
	cmd.Flags().StringVar(&storeWorkspace, "store-workspace", bootstrap.DefaultStoreWorkspace,
		"Name of the workspace Tenant records live in, created under --workspace-prefix "+
			"and bound to the Tenant API. Empty skips creating it.")
	cmd.Flags().StringVar(&tenantsWorkspace, "tenants-workspace", bootstrap.DefaultTenantsWorkspace,
		"Name of the workspace every tenant workspace is created under, bound to the "+
			"provisioner claim. Holds no tenancy objects. Empty skips creating it.")
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
