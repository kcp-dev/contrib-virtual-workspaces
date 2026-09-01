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

	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/contrib-virtual-workspaces/mcp/internal/access"
	"github.com/kcp-dev/contrib-virtual-workspaces/mcp/internal/mcp"
)

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "serve the MCP virtual workspace",
	Long: `Serve the Model Context Protocol at <prefix>/mcp, scoped per caller.

A global singleton, not one instance per workspace: an agent asks "what can I
work on" before it knows any workspace, so the answer has to span the fleet.

Unlike the other virtual workspaces this one is not a Kubernetes API. It is a
raw handler behind the same filter chain, because MCP is JSON-RPC over
streamable HTTP and has no GVR, no discovery and no resources to serve.

It holds no access graph. Which workspaces a caller may use comes from the
access virtual workspace over SelfClusterAccessReview, cached briefly per
caller — so there is one indexer in the deployment, not two that can disagree.

Per-workspace calls impersonate the caller: behind the front-proxy their bearer
token is consumed by the proxy and never arrives here, so this server's own
identity acts on their behalf and kcp authorizes every request as them.`,
	RunE: func(c *cobra.Command, _ []string) error { return runServe(c) },
}

func runServe(c *cobra.Command) error {
	if err := serverCfg.Validate(); err != nil {
		return err
	}

	restConfig, err := clientcmd.BuildConfigFromFlags("", serverCfg.Kcp.Kubeconfig)
	if err != nil {
		return fmt.Errorf("loading kubeconfig %q: %w", serverCfg.Kcp.Kubeconfig, err)
	}

	accessClient, err := access.New(access.Options{
		BaseURL:      serverCfg.Access.URL,
		CAFile:       serverCfg.Access.CAFile,
		Impersonator: restConfig,
		CacheTTL:     serverCfg.Access.CacheTTL,
	})
	if err != nil {
		return err
	}

	klog.FromContext(c.Context()).Info("serving the MCP virtual workspace",
		"path", mcp.RootPath,
		"accessURL", serverCfg.Access.URL,
		"cacheTTL", serverCfg.Access.CacheTTL,
	)

	return mcp.Serve(c.Context(), mcp.ServeOptions{
		SecureServing:  serverCfg.SecureServing,
		Authentication: serverCfg.Authentication,
		Access:         accessClient,
		Impersonator:   restConfig,
		Toolsets:       serverCfg.Toolsets,
	})
}
