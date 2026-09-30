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

// contribctl is the repo-wide CLI.
//
// It is organised around what a person does rather than around which
// component implements it: you log in, choose where you are working, look
// at what you can reach, and enable things there. Which virtual workspace
// answers any given command is an implementation detail the user should
// not have to know.
//
// The commands themselves belong to the components — every component that
// ships a client exposes it as importable cobra commands, and this binary
// only arranges them. The one piece of glue is the workspace resolver
// handed to the marketplace: it turns "here" into a URL using the tenancy
// selection, so the two components still never name each other.
package main

import (
	goflag "flag"
	"os"

	"github.com/spf13/cobra"

	"k8s.io/klog/v2"

	accesscli "github.com/kcp-dev/contrib-virtual-workspaces/access/pkg/cli"
	marketplacecli "github.com/kcp-dev/contrib-virtual-workspaces/marketplace/pkg/cli"
	tenancycli "github.com/kcp-dev/contrib-virtual-workspaces/tenancy/pkg/cli"
)

func main() {
	root := &cobra.Command{
		Use:   "contribctl",
		Short: "Work with kcp tenants, projects and the marketplace",
		Long: `contribctl is the client for kcp Platform.

Start by logging in and choosing where you are working:

  contribctl login --issuer https://dex.example.com
  contribctl use                     # pick a tenant and project
  contribctl tenants                 # what you can reach
  contribctl offers                  # what you could enable
  contribctl enable acme.things      # enable it here

Everything above needs only your own credential: the virtual workspaces
authenticate you and answer for the identity you actually hold. The
commands under "admin" are the other side — they write to kcp with a
kubeconfig and are for whoever runs the platform.`,
		SilenceUsage: true,
	}

	// klog's flags stay available — they are how you debug a command that
	// misbehaves — but hidden, because two dozen logging knobs in front of
	// someone looking for "how do I list my tenants" is not help.
	klogFlags := goflag.NewFlagSet("klog", goflag.ContinueOnError)
	klog.InitFlags(klogFlags)
	root.PersistentFlags().AddGoFlagSet(klogFlags)
	klogFlags.VisitAll(func(f *goflag.Flag) {
		if f.Name != "v" {
			_ = root.PersistentFlags().MarkHidden(f.Name)
		}
	})

	// Cobra prints commands in these groups, in this order, so the help
	// reads as the order you would do things in.
	const (
		groupAuth  = "auth"
		groupWork  = "work"
		groupAdmin = "admin"
	)
	root.AddGroup(
		&cobra.Group{ID: groupAuth, Title: "Getting in:"},
		&cobra.Group{ID: groupWork, Title: "Working:"},
		&cobra.Group{ID: groupAdmin, Title: "Running the platform:"},
	)

	add := func(group string, cmds ...*cobra.Command) {
		for _, c := range cmds {
			c.GroupID = group
			root.AddCommand(c)
		}
	}

	add(groupAuth,
		tenancycli.NewLoginCommand(),
		tenancycli.NewLogoutCommand(),
		tenancycli.NewWhoAmICommand(),
	)

	add(groupWork,
		tenancycli.NewUseCommand(),
		tenancycli.NewTenantsCommand(),
		tenancycli.NewProjectsCommand(),
		tenancycli.NewKubeconfigCommand(),
		marketplacecli.NewOffersCommand(),
		marketplacecli.NewEnableCommand(currentWorkspace),
	)

	add(groupAdmin,
		tenancycli.NewMembershipCommand(),
		tenancycli.NewAdminCommand(),
		accesscli.NewSCARToKubeconfigCommand(),
	)

	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

// currentWorkspace is the glue between the two components: the
// marketplace asks "where am I?", and the answer is whatever `use`
// selected. The marketplace never learns that the answer came from a
// tenancy project.
func currentWorkspace() (string, string, error) {
	selection := tenancycli.CurrentSelection()
	if selection.Empty() {
		return "", "", nil
	}
	return selection.URL, selection.String(), nil
}
