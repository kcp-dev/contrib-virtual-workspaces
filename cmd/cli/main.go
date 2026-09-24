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

// contribctl is the repo-wide CLI: every component that ships a client
// exposes it as an importable cobra command, and this binary mounts them
// all. `contribctl tenancy ...` is tenancyctl; `contribctl
// scar-to-kubeconfig` is the access component's kubeconfig helper. New
// components add one line here.
package main

import (
	goflag "flag"
	"os"

	"github.com/spf13/cobra"

	"k8s.io/klog/v2"

	accesscli "github.com/kcp-dev/contrib-virtual-workspaces/access/pkg/cli"
	tenancycli "github.com/kcp-dev/contrib-virtual-workspaces/tenancy/pkg/cli"
)

func main() {
	root := &cobra.Command{
		Use:   "contribctl",
		Short: "The kcp contrib virtual workspaces, one CLI",
		Long: `contribctl bundles every client this repository ships:

  tenancy             manage tenants, projects and memberships (tenancyctl)
  scar-to-kubeconfig  turn an access-VW review into a kubeconfig

Each subcommand is also available as its own binary; this is the same
code mounted under one root.`,
		SilenceUsage: true,
	}

	klogFlags := goflag.NewFlagSet("klog", goflag.ContinueOnError)
	klog.InitFlags(klogFlags)
	root.PersistentFlags().AddGoFlagSet(klogFlags)

	root.AddCommand(
		tenancycli.New("tenancy"),
		accesscli.NewSCARToKubeconfigCommand(),
	)

	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}
