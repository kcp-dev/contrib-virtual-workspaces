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
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	tenancyv1alpha1 "github.com/kcp-dev/contrib-virtual-workspaces/tenancy/sdk/apis/tenancy/v1alpha1"
)

// The user-facing half of the CLI: everything here answers from the
// caller's own credential through the virtual workspace, with no
// kubeconfig for kcp and no administrative rights. These are the
// commands mounted at the root of the repo-wide CLI.

// NewLoginCommand obtains a token from the identity provider and caches it.
func NewLoginCommand() *cobra.Command { return newLoginCommand() }

// NewLogoutCommand forgets the cached token.
func NewLogoutCommand() *cobra.Command { return newLogoutCommand() }

// NewWhoAmICommand shows the tenants and projects the caller can reach.
func NewWhoAmICommand() *cobra.Command { return newWhoAmICommand() }

// NewKubeconfigCommand writes a kubeconfig for the caller's projects.
func NewKubeconfigCommand() *cobra.Command { return newKubeconfigCommand() }

// NewAdminCommand groups everything that writes to kcp with a kubeconfig
// rather than answering from the caller's own credential: creating
// tenants and projects, bootstrapping an organization, seeding an
// environment. Whoever runs the platform uses these; a tenant does not.
func NewAdminCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "admin",
		Short: "Create tenants and projects, bootstrap and seed (needs a kubeconfig)",
		Long: `The platform side of the tenancy model.

These commands write to kcp directly — they need a kubeconfig with rights
in the workspace they touch, which is exactly what a tenant does not
have. The user-facing commands at the root need no kubeconfig at all.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	cmd.AddCommand(
		newTenantCommand(),
		newProjectCommand(),
		newOrgCommand(),
		newSeedCommand(),
	)
	return cmd
}

// NewTenantsCommand lists the tenants the caller is a member of.
func NewTenantsCommand() *cobra.Command {
	o := &userOptions{}
	cmd := &cobra.Command{
		Use:     "tenants",
		Aliases: []string{"tenant"},
		Short:   "List the tenants you can reach",
		Long: `List the tenants the calling identity is a member of, as the
tenancy virtual workspace reports them.

This is the user's view: it needs no kubeconfig for kcp, only your own
credential. To create a tenant, which is an administrator's job, see
"admin tenant create".`,
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
			current := CurrentSelection()
			rows := make([][]string, 0, len(review.Status.Tenants))
			for _, t := range review.Status.Tenants {
				rows = append(rows, []string{
					marker(current.Tenant == t.Name), t.Name, dash(t.DisplayName),
					dash(strings.Join(t.Roles, ",")), strconv.Itoa(len(t.Projects)),
				})
			}
			table([]string{"", "TENANT", "DISPLAY NAME", "ROLES", "PROJECTS"}, rows)
			return nil
		},
	}
	o.addFlags(cmd)
	return cmd
}

// NewProjectsCommand lists the projects the caller can reach.
func NewProjectsCommand() *cobra.Command {
	o := &userOptions{}
	var tenant string

	cmd := &cobra.Command{
		Use:     "projects",
		Aliases: []string{"project"},
		Short:   "List the projects you can reach",
		Long: `List the projects the calling identity can reach, across every
tenant, or only within one with --tenant.

A project is where work happens: it is the workspace your kubeconfig
context points at.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			review, err := o.review(cmd.Context())
			if err != nil {
				return err
			}
			current := CurrentSelection()
			rows := [][]string{}
			for _, t := range review.Status.Tenants {
				if tenant != "" && t.Name != tenant {
					continue
				}
				for _, p := range t.Projects {
					selected := current.Tenant == t.Name && current.Project == p.Name
					rows = append(rows, []string{
						marker(selected), t.Name, p.Name, dash(p.DisplayName),
						dash(strings.Join(rolesFor(t, p), ",")), dash(p.Endpoint),
					})
				}
			}
			if len(rows) == 0 {
				if tenant != "" {
					fmt.Printf("No projects you can reach in tenant %q.\n", tenant)
				} else {
					fmt.Println("You can reach no projects.")
				}
				return nil
			}
			table([]string{"", "TENANT", "PROJECT", "DISPLAY NAME", "ROLES", "ENDPOINT"}, rows)
			return nil
		},
	}
	o.addFlags(cmd)
	cmd.Flags().StringVar(&tenant, "tenant", "", "Only show projects of this tenant.")
	return cmd
}

// NewUseCommand selects the tenant and project later commands default to.
func NewUseCommand() *cobra.Command {
	o := &userOptions{}
	var clear bool

	cmd := &cobra.Command{
		Use:   "use [TENANT[/PROJECT]]",
		Short: "Choose the tenant and project to work in",
		Long: `Pick the project later commands act on, so they do not each need
--tenant and --project.

Given no argument it lists what you can reach and asks; given
TENANT/PROJECT it selects that directly. The choice is remembered in
` + "`" + `current.json` + "`" + ` next to the token cache, and "whoami" or "projects" shows
it marked with *.

Selecting nothing authorizes nothing: it is a convenience, and kcp still
decides what you may do in the workspace.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if clear {
				if err := ClearSelection(); err != nil {
					return err
				}
				fmt.Println("Selection cleared.")
				return nil
			}

			review, err := o.review(cmd.Context())
			if err != nil {
				return err
			}
			choices := flatten(review)
			if len(choices) == 0 {
				return fmt.Errorf("you can reach no project yet; `whoami` shows what you are a member of")
			}

			var chosen Selection
			if len(args) == 1 {
				chosen, err = matchChoice(choices, args[0])
			} else {
				chosen, err = promptForChoice(choices)
			}
			if err != nil {
				return err
			}

			if err := WriteSelection(chosen); err != nil {
				return err
			}
			fmt.Printf("Now using %s\n", chosen)
			if chosen.URL != "" {
				fmt.Printf("  %s\n", chosen.URL)
			}
			return nil
		},
	}
	o.addFlags(cmd)
	cmd.Flags().BoolVar(&clear, "clear", false, "Forget the current selection instead of choosing one.")
	return cmd
}

// flatten turns a review into the list of things that can be selected:
// every project, since a tenant workspace is a record store no member can
// enter (see defaultContext).
func flatten(review *tenancyv1alpha1.SelfTenancyReview) []Selection {
	out := []Selection{}
	for _, t := range review.Status.Tenants {
		for _, p := range t.Projects {
			out = append(out, Selection{Tenant: t.Name, Project: p.Name, URL: p.Endpoint})
		}
	}
	return out
}

// matchChoice resolves "tenant/project", or a bare tenant when it has
// exactly one project.
func matchChoice(choices []Selection, arg string) (Selection, error) {
	tenant, project, hasProject := strings.Cut(arg, "/")

	matches := []Selection{}
	for _, c := range choices {
		if c.Tenant != tenant {
			continue
		}
		if hasProject && c.Project != project {
			continue
		}
		matches = append(matches, c)
	}

	switch len(matches) {
	case 0:
		return Selection{}, fmt.Errorf("no project %q you can reach (try `projects`)", arg)
	case 1:
		return matches[0], nil
	default:
		names := make([]string, 0, len(matches))
		for _, m := range matches {
			names = append(names, m.String())
		}
		return Selection{}, fmt.Errorf("tenant %q has several projects, say which: %s",
			tenant, strings.Join(names, ", "))
	}
}

// promptForChoice lists the options and reads a number. Deliberately a
// plain numbered list read from stdin: it works over ssh, in a pipe with
// `echo 2 |`, and without a terminal library.
func promptForChoice(choices []Selection) (Selection, error) {
	current := CurrentSelection()
	fmt.Fprintln(os.Stderr, "Projects you can reach:")
	for i, c := range choices {
		fmt.Fprintf(os.Stderr, "  %s%2d) %s\n", marker(c.Tenant == current.Tenant && c.Project == current.Project), i+1, c)
	}
	fmt.Fprintf(os.Stderr, "Select [1-%d]: ", len(choices))

	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && strings.TrimSpace(line) == "" {
		return Selection{}, fmt.Errorf("nothing selected")
	}
	n, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil || n < 1 || n > len(choices) {
		return Selection{}, fmt.Errorf("not a choice between 1 and %d: %q", len(choices), strings.TrimSpace(line))
	}
	return choices[n-1], nil
}

// rolesFor reports the roles that reach a project: its own, plus the
// tenant-wide ones, which fan out to every project and are reported on
// the tenant rather than repeated on each.
func rolesFor(t tenancyv1alpha1.TenantClaim, p tenancyv1alpha1.ProjectClaim) []string {
	if len(p.Roles) > 0 {
		return p.Roles
	}
	return t.Roles
}

// marker flags the current selection in a listing.
func marker(selected bool) string {
	if selected {
		return "*"
	}
	return ""
}

// selectionFor resolves which tenant and project a command should act on:
// the flags when given, otherwise whatever `use` selected.
func selectionFor(tenant, project string) (Selection, error) {
	if tenant != "" {
		return Selection{Tenant: tenant, Project: project}, nil
	}
	current := CurrentSelection()
	if current.Empty() {
		return Selection{}, fmt.Errorf("no tenant selected: pass --tenant, or run `use` to choose one")
	}
	if project != "" {
		current.Project = project
		current.URL = ""
	}
	return current, nil
}
