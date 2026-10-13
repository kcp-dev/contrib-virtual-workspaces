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

package cli

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	accessbootstrap "github.com/kcp-dev/contrib-virtual-workspaces/access/pkg/bootstrap"
	"github.com/kcp-dev/contrib-virtual-workspaces/tenancy/pkg/bootstrap"
)

// The kcp objects the CLI reaches for directly, rather than through a
// typed client: they belong to kcp, not to this API group.
var (
	apiBindingGVR = schema.GroupVersionResource{Group: "apis.kcp.io", Version: "v1alpha2", Resource: "apibindings"}
	apiExportGVR  = schema.GroupVersionResource{Group: "apis.kcp.io", Version: "v1alpha2", Resource: "apiexports"}
)

func newOrgCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "org",
		Aliases: []string{"orgs"},
		Short:   "Manage organization workspaces (the workspaces tenancy objects live in)",
	}
	cmd.AddCommand(newOrgBootstrapCommand(), newOrgListCommand())
	return cmd
}

func newOrgBootstrapCommand() *cobra.Command {
	var (
		kubeconfig    string
		exportPath    string
		workspaceType string
	)

	cmd := &cobra.Command{
		Use:   "bootstrap PATH",
		Short: "Create a workspace and bind the platform exports into it",
		Long: `Make a workspace a platform workspace: create it if needed, bind
tenancy-platform (the Tenant API) and tenancy-provisioner (the claim that
lets tenant workspaces be created below it), and wait until Tenants are
servable there.

PATH is an absolute kcp workspace path, e.g. root:acme. The claim's
identity hash is read from the installed APIExport rather than typed by
hand, which is the part nobody should have to do manually.

The default platform workspace, ` + defaultWorkspace + `, is created by
"tenancy-vw init"; this is for additional ones. Tenant and project
workspaces are never bootstrapped this way — their WorkspaceType binds
them.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if kubeconfig == "" {
				kubeconfig = os.Getenv("KUBECONFIG")
			}
			if kubeconfig == "" {
				return fmt.Errorf("--kubeconfig is required, or set KUBECONFIG")
			}
			orgPath := strings.Trim(args[0], ":")

			cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
			if err != nil {
				return fmt.Errorf("load kubeconfig: %w", err)
			}

			result, err := exportsResult(cfg, exportPath)
			if err != nil {
				return err
			}

			orgCfg, err := accessbootstrap.CreateWorkspacePath(cmd.Context(), cfg, orgPath, workspaceType)
			if err != nil {
				return fmt.Errorf("resolve workspace path %s: %w", orgPath, err)
			}
			if err := bootstrap.BindStore(cmd.Context(), orgCfg, result); err != nil {
				return fmt.Errorf("bind the Tenant API into %s: %w", orgPath, err)
			}

			fmt.Printf("%s is ready for tenants\n", orgPath)
			fmt.Printf("  tenancyctl tenant create acme --workspace %s\n", orgPath)
			return nil
		},
	}
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", os.Getenv("KUBECONFIG"),
		"Path to the kubeconfig for kcp. Defaults to $KUBECONFIG.")
	cmd.Flags().StringVar(&exportPath, "export-path",
		bootstrap.DefaultWorkspacePrefix+":"+bootstrap.DefaultControllersWorkspace,
		"Workspace holding the tenancy APIExport.")
	cmd.Flags().StringVar(&workspaceType, "workspace-type", accessbootstrap.DefaultWorkspaceType,
		"WorkspaceType for any workspace this creates.")

	return cmd
}

func newOrgListCommand() *cobra.Command {
	o := &adminOptions{}
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "Show whether the selected workspace is bound to the platform export",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := o.restConfig()
			if err != nil {
				return err
			}
			dyn, err := dynamic.NewForConfig(cfg)
			if err != nil {
				return fmt.Errorf("build dynamic client: %w", err)
			}
			binding, err := dyn.Resource(apiBindingGVR).Get(cmd.Context(), bootstrap.ExportPlatform, metav1.GetOptions{})
			if err != nil {
				return fmt.Errorf("%s is not a platform workspace: %w "+
					"(run `tenancyctl org bootstrap %s`)", o.workspace, err, o.workspace)
			}
			phase, _, _ := unstructured.NestedString(binding.Object, "status", "phase")
			fmt.Printf("%s is bound to the tenancy-platform export (binding phase: %s)\n", o.workspace, dash(phase))
			return nil
		},
	}
	o.addFlags(cmd)
	return cmd
}

// exportsResult reconstructs what Bootstrap returned, by reading the
// installed provisioner export: its workspaces claim carries the identity
// hash, and the workspace it lives in carries the cluster reference.
func exportsResult(cfg *rest.Config, exportPath string) (*bootstrap.Result, error) {
	hash, err := exportWorkspacesIdentityHash(cfg, exportPath)
	if err != nil {
		return nil, err
	}
	cluster, err := exportsCluster(cfg, exportPath)
	if err != nil {
		return nil, err
	}
	return &bootstrap.Result{
		ExportsPath:            exportPath,
		ExportsCluster:         cluster,
		WorkspacesIdentityHash: hash,
	}, nil
}

// exportsCluster reads the logical cluster of the exports workspace.
func exportsCluster(cfg *rest.Config, exportPath string) (string, error) {
	scoped := rest.CopyConfig(cfg)
	scoped.Host = retargetHost(scoped.Host, exportPath)
	dyn, err := dynamic.NewForConfig(scoped)
	if err != nil {
		return "", fmt.Errorf("build dynamic client for %s: %w", exportPath, err)
	}
	lc, err := dyn.Resource(schema.GroupVersionResource{
		Group: "core.kcp.io", Version: "v1alpha1", Resource: "logicalclusters",
	}).Get(context.Background(), "cluster", metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("read the LogicalCluster of %s: %w", exportPath, err)
	}
	if name := lc.GetAnnotations()["kcp.io/cluster"]; name != "" {
		return name, nil
	}
	return "", fmt.Errorf("the LogicalCluster of %s carries no kcp.io/cluster annotation", exportPath)
}

// exportWorkspacesIdentityHash reads the identity hash off the installed
// provisioner export's workspaces claim, which is where init left it.
func exportWorkspacesIdentityHash(cfg *rest.Config, exportPath string) (string, error) {
	scoped := rest.CopyConfig(cfg)
	scoped.Host = retargetHost(scoped.Host, exportPath)

	dyn, err := dynamic.NewForConfig(scoped)
	if err != nil {
		return "", fmt.Errorf("build dynamic client for %s: %w", exportPath, err)
	}
	export, err := dyn.Resource(apiExportGVR).Get(context.Background(), bootstrap.ExportProvisioner, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("read the tenancy APIExport in %s (has `tenancy-vw init` run?): %w", exportPath, err)
	}
	claims, _, err := unstructured.NestedSlice(export.Object, "spec", "permissionClaims")
	if err != nil {
		return "", fmt.Errorf("read the export's permission claims: %w", err)
	}
	for _, c := range claims {
		claim, ok := c.(map[string]any)
		if !ok || claim["group"] != "tenancy.kcp.io" {
			continue
		}
		if hash, _ := claim["identityHash"].(string); hash != "" {
			return hash, nil
		}
	}
	return "", fmt.Errorf("the tenancy-provisioner APIExport in %s has no identity hash on its workspaces claim", exportPath)
}
