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

// Package cli holds the marketplace's user-facing commands: browsing the
// catalog, and enabling an offering in a workspace.
//
// Enabling is a plain kcp APIBinding the caller creates with their own
// credential, so these commands are sugar over surfaces that already
// exist — there is no marketplace API to call and nothing privileged in
// the path.
package cli

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"

	marketplacev1alpha1 "github.com/kcp-dev/contrib-virtual-workspaces/marketplace/sdk/apis/marketplace/v1alpha1"
	"github.com/kcp-dev/contrib-virtual-workspaces/pkg/clicontext"
)

// WorkspaceResolver reports the workspace URL a command should act on
// when no flag names one.
//
// It is a function rather than an import because the marketplace does not
// know what a tenant is: whoever assembles the CLI decides where "here"
// is — under the repo-wide CLI that is whatever `use` selected, and the
// two components still never name each other.
type WorkspaceResolver func() (url string, describe string, err error)

var scheme = runtime.NewScheme()

func init() {
	utilruntime.Must(marketplacev1alpha1.AddToScheme(scheme))
	utilruntime.Must(apisv1alpha2.AddToScheme(scheme))
}

// catalogOptions are the flags for reaching the marketplace virtual
// workspace, which any authenticated caller may read.
type catalogOptions struct {
	url        string
	kubeconfig string
	certFile   string
	keyFile    string
	tokenFile  string
	caFile     string
	insecure   bool
}

func (o *catalogOptions) addFlags(cmd *cobra.Command) {
	// Learned once by `login --url` and kept in the shared context, so
	// browsing the catalog needs no address of its own.
	stored := clicontext.Load()
	cmd.Flags().StringVar(&o.url, "url", envOr("MARKETPLACE_VW_URL", stored.ServiceURL("marketplace")),
		"Base URL of the marketplace virtual workspace. Defaults to the server "+
			"`login --url` stored, or $MARKETPLACE_VW_URL.")
	cmd.Flags().StringVar(&o.kubeconfig, "kubeconfig", os.Getenv("KUBECONFIG"),
		"Kubeconfig whose credential to use. Defaults to $KUBECONFIG.")
	cmd.Flags().StringVar(&o.certFile, "client-cert", "", "Client certificate to authenticate with.")
	cmd.Flags().StringVar(&o.keyFile, "client-key", "", "Key for --client-cert.")
	cmd.Flags().StringVar(&o.tokenFile, "token-file", "", "File holding a bearer token to authenticate with.")
	cmd.Flags().StringVar(&o.caFile, "certificate-authority", stored.CertificateAuthority,
		"CA bundle used to verify the virtual workspace's serving certificate.")
	cmd.Flags().BoolVar(&o.insecure, "insecure-skip-tls-verify", stored.Insecure,
		"Do not verify the serving certificate. For local testing only.")
}

func (o *catalogOptions) client() (client.Client, error) {
	if o.url == "" && o.kubeconfig == "" {
		return nil, fmt.Errorf("--url is required, or set MARKETPLACE_VW_URL")
	}

	cfg := &rest.Config{}
	if o.kubeconfig != "" {
		loaded, err := clientcmd.BuildConfigFromFlags("", o.kubeconfig)
		if err != nil {
			return nil, fmt.Errorf("load kubeconfig: %w", err)
		}
		cfg = loaded
		if i := strings.Index(cfg.Host, "/clusters/"); i >= 0 {
			cfg.Host = cfg.Host[:i]
		}
		cfg.Host = strings.TrimSuffix(cfg.Host, "/") + rootPath
	}
	if o.url != "" {
		cfg.Host = strings.TrimSuffix(o.url, "/")
		if !strings.HasSuffix(cfg.Host, rootPath) {
			cfg.Host += rootPath
		}
	}

	if o.certFile != "" || o.keyFile != "" {
		if o.certFile == "" || o.keyFile == "" {
			return nil, fmt.Errorf("--client-cert and --client-key must be given together")
		}
		cfg.CertFile, cfg.KeyFile = o.certFile, o.keyFile
	}
	if o.tokenFile != "" {
		raw, err := os.ReadFile(o.tokenFile)
		if err != nil {
			return nil, fmt.Errorf("read token file: %w", err)
		}
		cfg.BearerToken = strings.TrimSpace(string(raw))
	}
	if o.caFile != "" {
		cfg.CAFile, cfg.CAData = o.caFile, nil
	}
	if o.insecure {
		cfg.Insecure, cfg.CAFile, cfg.CAData = true, "", nil
	}

	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		return nil, fmt.Errorf("build client for %s: %w", cfg.Host, err)
	}
	return c, nil
}

// rootPath is where the catalog virtual workspace answers. Kept as a
// literal so this package does not drag the server in.
const rootPath = "/services/marketplace"

// NewOffersCommand lists the catalog.
func NewOffersCommand() *cobra.Command {
	o := &catalogOptions{}
	var wide bool

	cmd := &cobra.Command{
		Use:     "offers",
		Aliases: []string{"offer", "catalog"},
		Short:   "List the offerings you could enable",
		Long: `Browse the marketplace catalog.

Every entry carries the coordinates of the APIBinding that enabling it
would create — the provider's workspace and APIExport — so reading the
catalog is reading both the consent form and the binding. Any
authenticated caller may read it.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := o.client()
			if err != nil {
				return err
			}
			var offerings marketplacev1alpha1.CatalogOfferingList
			if err := c.List(cmd.Context(), &offerings); err != nil {
				return fmt.Errorf("list offerings: %w", err)
			}
			if len(offerings.Items) == 0 {
				fmt.Println("The catalog is empty.")
				return nil
			}

			header := []string{"OFFER", "DISPLAY NAME", "READY"}
			if wide {
				header = append(header, "EXPORT", "PROVIDER WORKSPACE", "REV", "CLAIMS")
			}
			rows := make([][]string, 0, len(offerings.Items))
			for _, off := range offerings.Items {
				row := []string{off.Name, dash(off.DisplayName), yesNo(off.Ready)}
				if wide {
					row = append(row, dash(off.Export), dash(off.WorkspacePath),
						dash(off.Revision), claimSummary(off.Claims))
				}
				rows = append(rows, row)
			}
			table(header, rows)
			return nil
		},
	}
	o.addFlags(cmd)
	cmd.Flags().BoolVarP(&wide, "wide", "o", false, "Also show the bind coordinates and claims.")
	return cmd
}

// NewEnableCommand enables an offering in a workspace by creating the
// APIBinding, as the caller.
func NewEnableCommand(resolve WorkspaceResolver) *cobra.Command {
	o := &catalogOptions{}
	var (
		workspace  string
		kubeconfig string
		name       string
	)

	cmd := &cobra.Command{
		Use:   "enable OFFER",
		Short: "Enable an offering in your workspace",
		Long: `Enable an offering: read its coordinates from the catalog and
create the APIBinding that enabling consists of.

The binding is created with your own credential, so kcp decides whether
it happens — you must be allowed to create APIBindings in the workspace,
and the provider must have published the offering, which is what grants
the bind permission. Nothing privileged is involved, and this command
does nothing the catalog did not already tell you.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := o.client()
			if err != nil {
				return err
			}
			var offering marketplacev1alpha1.CatalogOffering
			if err := c.Get(cmd.Context(), client.ObjectKey{Name: args[0]}, &offering); err != nil {
				if apierrors.IsNotFound(err) {
					return fmt.Errorf("no offering %q in the catalog (try `offers`)", args[0])
				}
				return fmt.Errorf("get offering %q: %w", args[0], err)
			}
			if offering.Export == "" || offering.WorkspacePath == "" {
				return fmt.Errorf("offering %q carries no bind coordinates yet; is it Ready?", args[0])
			}

			target, describe, err := targetWorkspace(workspace, resolve)
			if err != nil {
				return err
			}
			binding, err := bindingClient(target, kubeconfig, o)
			if err != nil {
				return err
			}

			if name == "" {
				name = offering.Offering
			}
			// Accepting the catalog's claims is the consent, and it happens
			// on the object being created here — so say what is being
			// accepted rather than doing it quietly.
			claims := acceptClaims(offering.Claims)
			for _, c := range offering.Claims {
				fmt.Printf("  accepting %s: %s\n", claimName(c), strings.Join(c.Verbs, ","))
			}
			obj := &apisv1alpha2.APIBinding{
				ObjectMeta: metav1.ObjectMeta{Name: name},
				Spec: apisv1alpha2.APIBindingSpec{
					Reference: apisv1alpha2.BindingReference{
						Export: &apisv1alpha2.ExportBindingReference{
							Path: offering.WorkspacePath,
							Name: offering.Export,
						},
					},
					PermissionClaims: claims,
				},
			}
			if err := binding.Create(cmd.Context(), obj); err != nil {
				if apierrors.IsAlreadyExists(err) {
					return fmt.Errorf("%q is already enabled in %s (binding %q)", args[0], describe, name)
				}
				return fmt.Errorf("create the binding in %s: %w%s", describe, err, enableHint(err))
			}
			fmt.Printf("enabled %s in %s\n", args[0], describe)
			fmt.Printf("  apibinding/%s → %s|%s\n", name, offering.WorkspacePath, offering.Export)
			return nil
		},
	}
	o.addFlags(cmd)
	cmd.Flags().StringVar(&workspace, "workspace", "",
		"URL of the workspace to enable in. Defaults to the one currently selected.")
	cmd.Flags().StringVar(&kubeconfig, "workspace-kubeconfig", "",
		"Kubeconfig whose credential to use against the workspace. Defaults to --kubeconfig.")
	cmd.Flags().StringVar(&name, "name", "", "Name for the APIBinding. Defaults to the offering's name.")
	return cmd
}

// targetWorkspace picks the workspace to write the binding into.
func targetWorkspace(flag string, resolve WorkspaceResolver) (string, string, error) {
	if flag != "" {
		return flag, flag, nil
	}
	if resolve == nil {
		return "", "", fmt.Errorf("--workspace is required")
	}
	url, describe, err := resolve()
	if err != nil {
		return "", "", err
	}
	if url == "" {
		return "", "", fmt.Errorf("no workspace selected: pass --workspace, or run `use` to choose one")
	}
	return url, describe, nil
}

// bindingClient talks to the workspace itself rather than to the virtual
// workspace: the binding is an ordinary object in the consumer's own
// cluster.
func bindingClient(url, kubeconfig string, o *catalogOptions) (client.Client, error) {
	cfg := &rest.Config{}
	if kubeconfig == "" {
		kubeconfig = o.kubeconfig
	}
	if kubeconfig != "" {
		loaded, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
		if err != nil {
			return nil, fmt.Errorf("load kubeconfig: %w", err)
		}
		cfg = loaded
	}
	cfg.Host = url

	if o.certFile != "" && o.keyFile != "" {
		cfg.CertFile, cfg.KeyFile = o.certFile, o.keyFile
	}
	if o.tokenFile != "" {
		raw, err := os.ReadFile(o.tokenFile)
		if err != nil {
			return nil, fmt.Errorf("read token file: %w", err)
		}
		cfg.BearerToken = strings.TrimSpace(string(raw))
	}
	if o.insecure {
		cfg.Insecure, cfg.CAFile, cfg.CAData = true, "", nil
	}

	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		return nil, fmt.Errorf("build client for %s: %w", url, err)
	}
	return c, nil
}

func enableHint(err error) string {
	if apierrors.IsForbidden(err) {
		return "\n\nkcp refused this: either you may not create APIBindings in that workspace " +
			"(an admin Membership is what grants it), or the offering is not published."
	}
	return ""
}

// acceptClaims turns the catalog's claim surface into the acceptance the
// APIBinding carries. Verbs and identity come from the catalog entry
// verbatim; the selector is the provider's suggested scope, or every
// object, which is what a binding means by default.
func acceptClaims(claims []marketplacev1alpha1.OfferingClaim) []apisv1alpha2.AcceptablePermissionClaim {
	var out []apisv1alpha2.AcceptablePermissionClaim
	for _, c := range claims {
		selector := apisv1alpha2.PermissionClaimSelector{MatchAll: true}
		if len(c.MatchLabels) > 0 {
			selector = apisv1alpha2.PermissionClaimSelector{
				LabelSelector: metav1.LabelSelector{MatchLabels: c.MatchLabels},
			}
		}
		out = append(out, apisv1alpha2.AcceptablePermissionClaim{
			ScopedPermissionClaim: apisv1alpha2.ScopedPermissionClaim{
				PermissionClaim: apisv1alpha2.PermissionClaim{
					GroupResource: apisv1alpha2.GroupResource{Group: c.Group, Resource: c.Resource},
					Verbs:         c.Verbs,
					IdentityHash:  c.IdentityHash,
				},
				Selector: selector,
			},
			State: apisv1alpha2.ClaimAccepted,
		})
	}
	return out
}

// claimName renders a claim the way kcp names one: resource.group, with
// the core group left implicit.
func claimName(c marketplacev1alpha1.OfferingClaim) string {
	if c.Group == "" {
		return c.Resource
	}
	return c.Resource + "." + c.Group
}

func claimSummary(claims []marketplacev1alpha1.OfferingClaim) string {
	if len(claims) == 0 {
		return "-"
	}
	parts := make([]string, 0, len(claims))
	for _, c := range claims {
		parts = append(parts, c.Resource)
	}
	return strings.Join(parts, ",")
}

// envOr is the usual precedence: an environment override, else whatever
// was stored at login.
func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func table(header []string, rows [][]string) {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	_, _ = fmt.Fprintln(w, strings.Join(header, "\t"))
	for _, row := range rows {
		_, _ = fmt.Fprintln(w, strings.Join(row, "\t"))
	}
	_ = w.Flush()
}
