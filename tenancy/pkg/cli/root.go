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

// Package cli implements tenancyctl, the day-to-day client for the tenancy
// model: provisioning organization workspaces, creating tenants and
// projects, granting and revoking access, and asking the virtual workspace
// what the caller can reach.
//
// It is deliberately a separate binary from the server: an administrator
// runs it with a kubeconfig, and an end user runs `whoami`/`kubeconfig`
// with nothing but their own credential and the virtual workspace's URL.
package cli

import (
	"context"
	goflag "flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"

	"github.com/kcp-dev/contrib-virtual-workspaces/tenancy/pkg/bootstrap"
	tenancyv1alpha1 "github.com/kcp-dev/contrib-virtual-workspaces/tenancy/sdk/apis/tenancy/v1alpha1"
)

// scheme carries only what the CLI touches.
var scheme = runtime.NewScheme()

func init() {
	utilruntime.Must(tenancyv1alpha1.AddToScheme(scheme))
}

// Execute runs tenancyctl.
func Execute() error {
	root := &cobra.Command{
		Use:   "tenancyctl",
		Short: "Manage kcp tenants, projects and access",
		Long: `tenancyctl is the client for the kcp tenancy model.

Administrators use it against an organization workspace — by default
` + defaultWorkspace + `, the one ` + "`tenancy-vw init`" + ` creates — to
create tenants and projects and to grant access. Users run "login" once to
get a JWT from the identity provider, then "whoami" and "kubeconfig"
against the tenancy virtual workspace to discover what they can reach —
with no kubeconfig for kcp itself and no credential flags.`,
		SilenceUsage: true,
	}

	klogFlags := goflag.NewFlagSet("klog", goflag.ContinueOnError)
	klog.InitFlags(klogFlags)
	root.PersistentFlags().AddGoFlagSet(klogFlags)

	root.AddCommand(
		newLoginCommand(),
		newLogoutCommand(),
		newTenantCommand(),
		newProjectCommand(),
		newGrantCommand(),
		newRevokeCommand(),
		newWhoAmICommand(),
		newKubeconfigCommand(),
		newOrgCommand(),
	)

	return root.Execute()
}

// defaultWorkspace is the tenant store: where Tenant records live unless
// told otherwise. Projects and Memberships are not here — they live inside
// each tenant's own workspace, which the CLI reaches by following the
// Tenant's reported URL.
var defaultWorkspace = bootstrap.DefaultWorkspacePrefix + ":" + bootstrap.DefaultStoreWorkspace

// adminOptions are the flags every command that talks to kcp shares.
type adminOptions struct {
	kubeconfig string
	workspace  string
}

func (o *adminOptions) addFlags(cmd *cobra.Command) {
	cmd.PersistentFlags().StringVar(&o.kubeconfig, "kubeconfig", os.Getenv("KUBECONFIG"),
		"Path to the kubeconfig for kcp. Defaults to $KUBECONFIG.")
	cmd.PersistentFlags().StringVar(&o.workspace, "workspace", defaultWorkspace,
		"Workspace holding the Tenant records.")
}

// restConfig returns a config pointed at the selected workspace.
func (o *adminOptions) restConfig() (*rest.Config, error) {
	if o.kubeconfig == "" {
		return nil, fmt.Errorf("--kubeconfig is required, or set KUBECONFIG")
	}
	cfg, err := clientcmd.BuildConfigFromFlags("", o.kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("load kubeconfig: %w", err)
	}
	if o.workspace != "" {
		cfg.Host = retargetHost(cfg.Host, o.workspace)
	}
	return cfg, nil
}

func (o *adminOptions) client() (client.Client, error) {
	cfg, err := o.restConfig()
	if err != nil {
		return nil, err
	}
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		return nil, fmt.Errorf("build client for %s: %w", o.workspace, err)
	}
	return c, nil
}

// tenantClient returns a client for one tenant's OWN workspace, which is
// where its Projects and Memberships live. The platform workspace only
// holds the Tenant object; following it is a deliberate hop, because the
// tenant tier is a different logical cluster with a different binding.
func (o *adminOptions) tenantClient(ctx context.Context, tenant string) (client.Client, error) {
	platform, err := o.client()
	if err != nil {
		return nil, err
	}
	var t tenancyv1alpha1.Tenant
	if err := platform.Get(ctx, client.ObjectKey{Name: tenant}, &t); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("no tenant %q in %s (try `tenancyctl tenant list`)", tenant, o.workspace)
		}
		return nil, fmt.Errorf("get tenant %q: %w", tenant, err)
	}
	if t.Status.URL == "" {
		return nil, fmt.Errorf("tenant %q has no workspace yet (phase %s) — is the operator running?",
			tenant, dash(string(t.Status.Phase)))
	}

	cfg, err := o.restConfig()
	if err != nil {
		return nil, err
	}
	cfg.Host = t.Status.URL
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		return nil, fmt.Errorf("build client for tenant %q at %s: %w", tenant, t.Status.URL, err)
	}
	return c, nil
}

// retargetHost swaps the workspace path of a kcp cluster URL.
func retargetHost(host, workspacePath string) string {
	if i := strings.Index(host, "/clusters/"); i >= 0 {
		host = host[:i]
	}
	return strings.TrimSuffix(host, "/") + "/clusters/" + workspacePath
}

// table writes aligned columns to stdout, the one output shape every list
// command here needs.
func table(header []string, rows [][]string) {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	_, _ = fmt.Fprintln(w, strings.Join(header, "\t"))
	for _, row := range rows {
		_, _ = fmt.Fprintln(w, strings.Join(row, "\t"))
	}
	_ = w.Flush()
}

// dash renders an empty value as "-" so columns never look truncated.
func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
