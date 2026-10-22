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
	"os"
	"time"

	"github.com/spf13/cobra"

	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/contrib-virtual-workspaces/mcp/pkg/bootstrap"
)

var initOpts struct {
	kubeconfig string
	bootstrap.Options
}

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Install the mcp APIExport a consumer enables by binding",
	Long: `Install this component's kcp-side objects.

One APIExport, and it is the enablement mechanism rather than an API: MCP
is an endpoint, so there is nothing for a consumer to bind in order to
"get" it. What they are consenting to is letting the MCP server act as
them in their workspace, and the export's permission claims are exactly
the RBAC the operator needs to grant itself that. Binding is the consent;
deleting the binding revokes it.

Safe to run repeatedly: everything is created if absent and brought to
the committed shape otherwise.`,
	Args:         cobra.NoArgs,
	SilenceUsage: true,
	RunE: func(c *cobra.Command, _ []string) error {
		cfg, err := clientcmd.BuildConfigFromFlags("", initOpts.kubeconfig)
		if err != nil {
			return fmt.Errorf("loading kubeconfig %q: %w", initOpts.kubeconfig, err)
		}

		result, err := bootstrap.Init(c.Context(), cfg, initOpts.Options)
		if err != nil {
			return err
		}

		logger := klog.FromContext(c.Context())
		logger.Info("mcp installed",
			"workspace", result.WorkspacePath,
			"exportCluster", result.ExportCluster,
			"sliceURLs", result.SliceURLs)
		logger.Info("a consumer enables MCP by binding this export",
			"path", result.WorkspacePath, "export", bootstrap.ExportName)
		return nil
	},
}

func init() {
	fs := initCmd.Flags()
	fs.StringVar(&initOpts.kubeconfig, "kubeconfig", os.Getenv("KUBECONFIG"),
		"Path to the kubeconfig for the target kcp. Defaults to $KUBECONFIG.")
	fs.StringVar(&initOpts.WorkspacePrefix, "workspace-prefix", bootstrap.DefaultWorkspacePrefix,
		"Parent path the component's workspace is created under.")
	fs.StringVar(&initOpts.ControllersWorkspace, "controllers-workspace", bootstrap.DefaultControllersWorkspace,
		"Name of the workspace this component owns, created under --workspace-prefix.")
	fs.StringVar(&initOpts.WorkspaceType, "workspace-type", bootstrap.DefaultWorkspaceType,
		"WorkspaceType for workspaces created here.")
	fs.StringSliceVar(&initOpts.ServerUsers, "server-user", nil,
		"Additional User to grant the operator role to, repeatable. kcp-operator mounts a "+
			"client certificate rather than a ServiceAccount token, so pass its common name, "+
			"e.g. --server-user=mcp-vw.")
	fs.StringSliceVar(&initOpts.ServerGroups, "server-group", nil,
		"Additional Group to grant the operator role to, repeatable.")
	fs.DurationVar(&initOpts.Timeout, "timeout", 3*time.Minute,
		"Bounds the apply-and-verify loop.")
}
