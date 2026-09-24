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

// Package cli holds the access component's user-facing commands, shaped so
// the repo-wide CLI can mount them and the standalone binaries stay thin
// wrappers.
package cli

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// NewSCARToKubeconfigCommand builds the scar-to-kubeconfig command: it
// calls the access virtual workspace's SelfClusterAccessReview endpoint
// with a bearer token and writes a kubeconfig with one context per
// authorized cluster.
func NewSCARToKubeconfigCommand() *cobra.Command {
	var (
		scarURL  string
		token    string
		output   string
		insecure bool
	)

	cmd := &cobra.Command{
		Use:   "scar-to-kubeconfig",
		Short: "Turn a SelfClusterAccessReview answer into a kubeconfig",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if token == "" {
				return fmt.Errorf("--token is required")
			}

			clusters, err := callSCAR(cmd.Context(), scarURL, token, insecure)
			if err != nil {
				return fmt.Errorf("SCAR call failed: %w", err)
			}
			if len(clusters) == 0 {
				return fmt.Errorf("SCAR returned no clusters for this identity (check RBAC bindings)")
			}

			kubeconfig := buildKubeconfig(clusters, token, insecure)
			data, err := clientcmd.Write(*kubeconfig)
			if err != nil {
				return fmt.Errorf("serialize kubeconfig: %w", err)
			}
			if err := os.WriteFile(output, data, 0o600); err != nil {
				return fmt.Errorf("write %s: %w", output, err)
			}

			fmt.Fprintf(os.Stderr, "wrote %s with %d cluster context(s)\n", output, len(clusters))
			fmt.Fprintf(os.Stderr, "next: kubernetes-mcp-server --kubeconfig=%s --cluster-provider=kcp\n", output)
			return nil
		},
	}

	cmd.Flags().StringVar(&scarURL, "scar-url",
		"https://localhost:9443/services/access/apis/access.contrib.kcp.io/v1alpha1/selfclusteraccessreviews",
		"SCAR endpoint URL")
	cmd.Flags().StringVar(&token, "token", "", "Bearer token (required)")
	cmd.Flags().StringVar(&output, "output", "scar.kubeconfig", "Output kubeconfig path")
	cmd.Flags().BoolVar(&insecure, "insecure", false, "Skip TLS verification for cluster endpoints")

	return cmd
}

type scarResponse struct {
	Status struct {
		Clusters []struct {
			ClusterName string `json:"clusterName"`
			Endpoint    string `json:"endpoint"`
		} `json:"clusters"`
	} `json:"status"`
}

type scarCluster struct {
	Name     string
	Endpoint string
}

func callSCAR(ctx context.Context, scarURL, token string, insecure bool) ([]scarCluster, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	if insecure {
		client.Transport = &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // opt-in, for a local server
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, scarURL, strings.NewReader("{}"))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("POST %s: %w", scarURL, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("SCAR returned %d: %s", resp.StatusCode, string(body))
	}

	var scar scarResponse
	if err := json.Unmarshal(body, &scar); err != nil {
		return nil, fmt.Errorf("decode SCAR response: %w", err)
	}

	out := make([]scarCluster, len(scar.Status.Clusters))
	for i, c := range scar.Status.Clusters {
		out[i] = scarCluster{Name: c.ClusterName, Endpoint: c.Endpoint}
	}
	return out, nil
}

func buildKubeconfig(clusters []scarCluster, token string, insecure bool) *clientcmdapi.Config {
	config := clientcmdapi.NewConfig()

	for _, c := range clusters {
		cluster := clientcmdapi.NewCluster()
		cluster.Server = c.Endpoint
		if insecure {
			cluster.InsecureSkipTLSVerify = true
		}
		config.Clusters[c.Name] = cluster

		ctx := clientcmdapi.NewContext()
		ctx.Cluster = c.Name
		ctx.AuthInfo = "scar-user"
		config.Contexts[c.Name] = ctx
	}

	user := clientcmdapi.NewAuthInfo()
	user.Token = token
	config.AuthInfos["scar-user"] = user

	config.CurrentContext = clusters[0].Name

	return config
}
