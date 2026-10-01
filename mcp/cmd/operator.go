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
	"strings"

	"github.com/spf13/cobra"

	"k8s.io/client-go/tools/clientcmd"

	"github.com/kcp-dev/contrib-virtual-workspaces/mcp/pkg/bootstrap"
	"github.com/kcp-dev/contrib-virtual-workspaces/mcp/pkg/operator"
	"github.com/kcp-dev/contrib-virtual-workspaces/mcp/pkg/toolsets"
)

var operatorOpts struct {
	kubeconfig    string
	workspacePath string
	operator.Options
}

var operatorCmd = &cobra.Command{
	Use:   "operator",
	Short: "Grant MCP what it needs in each workspace that enabled it",
	Long: `Reconcile the workspaces that have enabled MCP.

A workspace enables MCP by binding this component's APIExport, which is
what puts it on the export's endpoint slice and what grants this operator
the claims it spends. For each such workspace it writes one thing — the
right for the MCP server to impersonate callers there — and then records
an MCPEndpoint so whoever enabled it can see that it worked and where to
point a client.

Impersonation is all the server gets, and all it needs: every resource it
touches it touches as the caller, so kcp authorizes each request against
their rights.`,
	Args:         cobra.NoArgs,
	SilenceUsage: true,
	RunE: func(c *cobra.Command, _ []string) error {
		cfg, err := clientcmd.BuildConfigFromFlags("", operatorOpts.kubeconfig)
		if err != nil {
			return fmt.Errorf("loading kubeconfig %q: %w", operatorOpts.kubeconfig, err)
		}
		// The endpoint slice lives in the workspace this component owns, so
		// the client has to be pointed at it. An operator-minted kubeconfig
		// addresses the root workspace, where there is no slice of ours and
		// so nothing to discover — which looks exactly like a working
		// operator that never reconciles anything.
		if operatorOpts.workspacePath != "" {
			cfg.Host = retargetHost(cfg.Host, operatorOpts.workspacePath)
		}
		operatorOpts.RestConfig = cfg
		return operator.Run(c.Context(), operatorOpts.Options)
	},
}

func init() {
	fs := operatorCmd.Flags()
	fs.StringVar(&operatorOpts.kubeconfig, "kubeconfig", os.Getenv("KUBECONFIG"),
		"Path to the kubeconfig for the target kcp. Defaults to $KUBECONFIG.")
	fs.StringVar(&operatorOpts.Slice, "apiexport-endpointslice", bootstrap.ExportName,
		"APIExportEndpointSlice following the mcp export. The clusters on it are the "+
			"workspaces that enabled MCP.")
	fs.StringVar(&operatorOpts.ServerURL, "server-url", "",
		"Where an MCP client connects, reported to consumers in their MCPEndpoint, "+
			"e.g. https://kcp.example.com/services/mcp.")
	fs.StringVar(&operatorOpts.ImpersonateUser, "impersonate-user", "",
		"The identity the MCP server presents, and so the subject the impersonation "+
			"grant names. Required: it is what the grant is for.")
	fs.StringSliceVar(&operatorOpts.Toolsets, "toolsets", toolsets.Default(),
		"Toolsets the server exposes, reported to consumers.")
	fs.StringVar(&operatorOpts.workspacePath, "workspace-path", "",
		"Workspace holding this component's APIExport and endpoint slice, "+
			"e.g. root:mcp:controllers. Empty uses the kubeconfig's own cluster "+
			"URL unchanged, which only works if it already points there.")
}

// retargetHost replaces whatever cluster a kubeconfig addresses with one
// workspace path, keeping the server.
func retargetHost(host, workspacePath string) string {
	if i := strings.Index(host, "/clusters/"); i >= 0 {
		host = host[:i]
	}
	return strings.TrimSuffix(host, "/") + "/clusters/" + workspacePath
}
