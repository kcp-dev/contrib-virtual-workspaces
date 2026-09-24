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

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kcp-dev/contrib-virtual-workspaces/tenancy/pkg/virtual/selftenancyreview"
	tenancyv1alpha1 "github.com/kcp-dev/contrib-virtual-workspaces/tenancy/sdk/apis/tenancy/v1alpha1"
)

// userOptions are the flags an end user needs: the virtual workspace's
// address and their own credential. Notably not a kcp kubeconfig — the
// point of the review is to find out what to put in one.
type userOptions struct {
	url        string
	certFile   string
	keyFile    string
	tokenFile  string
	caFile     string
	insecure   bool
	kubeconfig string
	// tokenCache is where `tenancyctl login` left a JWT. It is the
	// fallback when no credential is named explicitly, which is the whole
	// point of having logged in.
	tokenCache string
	issuer     string
	clientID   string
}

func (o *userOptions) addFlags(cmd *cobra.Command) {
	cmd.Flags().StringVar(&o.url, "url", os.Getenv("TENANCY_VW_URL"),
		"Base URL of the tenancy virtual workspace, e.g. https://kcp.example.com. "+
			"Defaults to $TENANCY_VW_URL.")
	cmd.Flags().StringVar(&o.certFile, "client-cert", "", "Client certificate to authenticate with.")
	cmd.Flags().StringVar(&o.keyFile, "client-key", "", "Key for --client-cert.")
	cmd.Flags().StringVar(&o.tokenFile, "token-file", "", "File holding a bearer token to authenticate with.")
	cmd.Flags().StringVar(&o.caFile, "certificate-authority", "",
		"CA bundle used to verify the virtual workspace's serving certificate.")
	cmd.Flags().BoolVar(&o.insecure, "insecure-skip-tls-verify", false,
		"Do not verify the virtual workspace's serving certificate. For local testing only.")
	cmd.Flags().StringVar(&o.kubeconfig, "kubeconfig", "",
		"Take credentials from this kubeconfig's current context instead of --client-cert/--token-file.")
	cmd.Flags().StringVar(&o.tokenCache, "token-cache", defaultTokenCachePath(),
		"Where `tenancyctl login` cached a token. Used when no other credential is given.")
	cmd.Flags().StringVar(&o.issuer, "issuer", envOr("TENANCY_OIDC_ISSUER", ""),
		"Which cached login to use, when more than one issuer is cached.")
	cmd.Flags().StringVar(&o.clientID, "client-id", envOr("TENANCY_OIDC_CLIENT_ID", "kcp"),
		"Client id of the cached login to use.")
}

// restConfig builds the connection to the virtual workspace's root path.
//
// Behind kcp's front-proxy the virtual workspace answers on the same host
// as kcp itself, so an explicitly given kubeconfig is enough to find it.
// A virtual workspace run as a separate process — every local test — is
// somewhere else, and has to be named.
func (o *userOptions) restConfig() (*rest.Config, error) {
	if o.url == "" && o.kubeconfig == "" {
		return nil, fmt.Errorf(`--url is required, or set TENANCY_VW_URL.

It is the address the tenancy virtual workspace serves on:
  behind kcp's front-proxy   the same host as kcp, e.g. https://kcp.example.com
                             (or pass --kubeconfig and it is taken from there)
  run locally                whatever --secure-port it was started with,
                             e.g. https://localhost:9445`)
	}

	var cfg *rest.Config
	if o.kubeconfig != "" {
		loaded, err := clientcmd.BuildConfigFromFlags("", o.kubeconfig)
		if err != nil {
			return nil, fmt.Errorf("load kubeconfig: %w", err)
		}
		cfg = loaded
		// The kubeconfig points into a workspace; the virtual workspace
		// hangs off the root of the same server.
		if i := strings.Index(cfg.Host, "/clusters/"); i >= 0 {
			cfg.Host = cfg.Host[:i]
		}
	} else {
		cfg = &rest.Config{}
	}

	if o.url != "" {
		cfg.Host = strings.TrimSuffix(o.url, "/")
	}
	// The review lives under the virtual workspace's root path; everything
	// else about the request is ordinary Kubernetes API traffic.
	cfg.Host = strings.TrimSuffix(cfg.Host, selftenancyreview.RootPath) + selftenancyreview.RootPath

	if o.certFile != "" || o.keyFile != "" {
		if o.certFile == "" || o.keyFile == "" {
			return nil, fmt.Errorf("--client-cert and --client-key must be given together")
		}
		cfg.CertFile = o.certFile
		cfg.KeyFile = o.keyFile
	}
	if o.tokenFile != "" {
		raw, err := os.ReadFile(o.tokenFile)
		if err != nil {
			return nil, fmt.Errorf("read token file: %w", err)
		}
		cfg.BearerToken = strings.TrimSpace(string(raw))
	}

	// Nothing explicit was given, so fall back to whatever `login` cached.
	if cfg.CertFile == "" && cfg.BearerToken == "" && o.kubeconfig == "" && o.tokenCache != "" {
		if o.issuer != "" {
			cfg.BearerToken = cachedToken(o.tokenCache, o.issuer, o.clientID)
		} else if token, ok := anyCachedToken(o.tokenCache); ok {
			cfg.BearerToken = token
		}
		if cfg.BearerToken == "" {
			return nil, fmt.Errorf("no credential: pass --client-cert/--client-key or --token-file, " +
				"or run `tenancyctl login` first")
		}
	}
	if o.caFile != "" {
		cfg.CAFile = o.caFile
		cfg.CAData = nil
	}
	if o.insecure {
		cfg.Insecure = true
		cfg.CAFile = ""
		cfg.CAData = nil
	}
	return cfg, nil
}

// review asks the virtual workspace what the caller can reach.
func (o *userOptions) review(ctx context.Context) (*tenancyv1alpha1.SelfTenancyReview, error) {
	cfg, err := o.restConfig()
	if err != nil {
		return nil, err
	}
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		return nil, fmt.Errorf("build client for %s: %w", cfg.Host, err)
	}
	out := &tenancyv1alpha1.SelfTenancyReview{}
	if err := c.Create(ctx, out); err != nil {
		return nil, fmt.Errorf("ask %s who you are: %w%s", cfg.Host, err, hint(err))
	}
	return out, nil
}

func newWhoAmICommand() *cobra.Command {
	o := &userOptions{}
	cmd := &cobra.Command{
		Use:   "whoami",
		Short: "Show the tenants and projects you can reach",
		Long: `Ask the tenancy virtual workspace for a SelfTenancyReview: the
tenants and projects the calling identity is a member of, the roles it
holds, and the URL each workspace is reachable at.

This needs no kubeconfig for kcp — only your own credential.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			review, err := o.review(cmd.Context())
			if err != nil {
				return err
			}
			if len(review.Status.Tenants) == 0 {
				fmt.Println("You are not a member of any tenant.")
				return nil
			}
			rows := [][]string{}
			for _, t := range review.Status.Tenants {
				rows = append(rows, []string{
					t.Name, "-", dash(t.DisplayName),
					dash(strings.Join(t.Roles, ",")), dash(t.Endpoint),
				})
				for _, p := range t.Projects {
					rows = append(rows, []string{
						t.Name, p.Name, dash(p.DisplayName),
						dash(strings.Join(p.Roles, ",")), dash(p.Endpoint),
					})
				}
			}
			table([]string{"TENANT", "PROJECT", "DISPLAY NAME", "ROLES", "ENDPOINT"}, rows)
			return nil
		},
	}
	o.addFlags(cmd)
	return cmd
}

func newKubeconfigCommand() *cobra.Command {
	o := &userOptions{}
	var (
		output   string
		context_ string
	)

	cmd := &cobra.Command{
		Use:   "kubeconfig",
		Short: "Write a kubeconfig with a context per tenant and project you can reach",
		Long: `Ask the virtual workspace what you can reach and turn the answer
into a kubeconfig: one context per tenant and per project, each pointing at
that workspace's own URL, all sharing your credential.

This is the closest thing to a login: after running it, "kubectl
--context=<tenant>" talks to that tenant's workspace directly.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			review, err := o.review(cmd.Context())
			if err != nil {
				return err
			}
			if len(review.Status.Tenants) == 0 {
				return fmt.Errorf("you are not a member of any tenant; there is nothing to write")
			}

			cfg, err := o.restConfig()
			if err != nil {
				return err
			}

			kubeconfig := clientcmdapi.NewConfig()
			authName := "tenancy"
			auth := clientcmdapi.NewAuthInfo()
			switch {
			case cfg.CertFile != "":
				auth.ClientCertificate = cfg.CertFile
				auth.ClientKey = cfg.KeyFile
			case cfg.BearerToken != "":
				auth.Token = cfg.BearerToken
			}
			kubeconfig.AuthInfos[authName] = auth

			add := func(name, endpoint string) {
				if endpoint == "" {
					return
				}
				cluster := clientcmdapi.NewCluster()
				cluster.Server = endpoint
				cluster.InsecureSkipTLSVerify = o.insecure
				if !o.insecure && o.caFile != "" {
					cluster.CertificateAuthority = o.caFile
				}
				kubeconfig.Clusters[name] = cluster

				ctx := clientcmdapi.NewContext()
				ctx.Cluster = name
				ctx.AuthInfo = authName
				kubeconfig.Contexts[name] = ctx
			}

			for _, t := range review.Status.Tenants {
				add(t.Name, t.Endpoint)
				for _, p := range t.Projects {
					add(t.Name+"-"+p.Name, p.Endpoint)
				}
			}
			if len(kubeconfig.Contexts) == 0 {
				return fmt.Errorf("no workspace in the answer carried an endpoint URL; " +
					"is the virtual workspace started with --endpoint-base?")
			}

			if context_ != "" {
				if _, ok := kubeconfig.Contexts[context_]; !ok {
					return fmt.Errorf("no context named %q in the answer", context_)
				}
				kubeconfig.CurrentContext = context_
			} else {
				kubeconfig.CurrentContext = review.Status.Tenants[0].Name
			}

			if output == "" || output == "-" {
				raw, err := clientcmd.Write(*kubeconfig)
				if err != nil {
					return fmt.Errorf("render kubeconfig: %w", err)
				}
				fmt.Print(string(raw))
				return nil
			}
			if err := clientcmd.WriteToFile(*kubeconfig, output); err != nil {
				return fmt.Errorf("write %s: %w", output, err)
			}
			fmt.Printf("wrote %s with %d contexts (current: %s)\n",
				output, len(kubeconfig.Contexts), kubeconfig.CurrentContext)
			return nil
		},
	}
	o.addFlags(cmd)
	cmd.Flags().StringVarP(&output, "output", "o", "",
		"File to write. Empty or - prints to stdout.")
	cmd.Flags().StringVar(&context_, "current-context", "",
		"Which context to mark current. Defaults to the first tenant.")

	return cmd
}

// hint turns the three failures every new caller hits into an instruction.
// They are all indistinguishable from "it is broken" without one.
func hint(err error) string {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "x509") || strings.Contains(msg, "certificate signed by unknown authority"):
		return "\n\nThe virtual workspace's serving certificate is not trusted. Pass " +
			"--certificate-authority with the CA that signed it, or " +
			"--insecure-skip-tls-verify for a self-signed local one."
	case strings.Contains(msg, "Unauthorized"):
		return "\n\nYour certificate was not accepted. It must be signed by the CA the " +
			"virtual workspace was started with (--client-ca-file), and still be valid: " +
			"openssl verify -CAfile <that ca.crt> <your.crt>"
	case strings.Contains(msg, "connection refused"):
		return "\n\nNothing is listening there. Check --url against the --secure-port the " +
			"virtual workspace was started with."
	default:
		return ""
	}
}
