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
	"fmt"

	"github.com/spf13/cobra"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	tenancyv1alpha1 "github.com/kcp-dev/contrib-virtual-workspaces/tenancy/sdk/apis/tenancy/v1alpha1"
)

// NewMembershipCommand groups the grant lifecycle under one noun:
// `membership` lists, `membership add` grants, `membership remove`
// revokes.
//
// Unlike the user-facing commands, these need a kubeconfig for kcp: a
// Membership lives in its tenant's workspace, which is the record store
// no member can enter, so managing access is an administrator's job by
// construction rather than by a check.
func NewMembershipCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "membership",
		Aliases: []string{"memberships"},
		Short:   "List, grant and revoke access to a tenant or project",
		Long: `A Membership records what an identity may do, and the operator
materializes it as RBAC in the tenant's project workspaces.

There is no user registry to enrol anyone into: issuing credentials is
the identity provider's job, and a Membership only names the identity it
already issues.

Run without a subcommand it lists the grants in the tenant.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}

	cmd.AddCommand(
		newMembershipListCommand(),
		newMembershipAddCommand(),
		newMembershipRemoveCommand(),
	)
	return cmd
}

// tenantFlag adds --tenant, defaulting to whatever `use` selected, so the
// admin commands compose with the user-facing ones.
func tenantFlag(cmd *cobra.Command, tenant *string) {
	cmd.Flags().StringVar(tenant, "tenant", "",
		"Tenant to act on. Defaults to the one `use` selected.")
}

func newMembershipListCommand() *cobra.Command {
	o := &adminOptions{}
	var tenant string

	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List the grants in a tenant",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			selected, err := selectionFor(tenant, "")
			if err != nil {
				return err
			}
			c, err := o.tenantClient(cmd.Context(), selected.Tenant)
			if err != nil {
				return err
			}
			var memberships tenancyv1alpha1.MembershipList
			if err := c.List(cmd.Context(), &memberships); err != nil {
				return fmt.Errorf("list memberships: %w", err)
			}
			if len(memberships.Items) == 0 {
				fmt.Printf("No grants in tenant %s.\n", selected.Tenant)
				return nil
			}
			rows := make([][]string, 0, len(memberships.Items))
			for _, m := range memberships.Items {
				rows = append(rows, []string{
					m.Name, string(m.Spec.Subject.Kind), m.Spec.Subject.Name,
					string(m.Spec.Role), dash(m.Spec.Project),
					string(dashPhase(m.Status.Phase)),
				})
			}
			table([]string{"NAME", "KIND", "SUBJECT", "ROLE", "PROJECT", "PHASE"}, rows)
			return nil
		},
	}
	o.addFlags(cmd)
	tenantFlag(cmd, &tenant)
	return cmd
}

func newMembershipAddCommand() *cobra.Command {
	o := &adminOptions{}
	var (
		role    string
		tenant  string
		project string
		asGroup bool
	)

	cmd := &cobra.Command{
		Use:     "add SUBJECT",
		Aliases: []string{"grant"},
		Short:   "Grant a user or group a role",
		Long: `Grant access by creating a Membership.

SUBJECT is matched verbatim against the identity kcp authenticates: the
CommonName of a client certificate, or the username an OIDC token maps
to. With --group it is matched against the caller's groups instead.

Without --project the grant is tenant-wide and reaches every project,
including ones created later. Re-running with a different --role changes
the grant in place rather than adding a second one.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			switch tenancyv1alpha1.Role(role) {
			case tenancyv1alpha1.RoleView, tenancyv1alpha1.RoleEdit, tenancyv1alpha1.RoleAdmin:
			default:
				return fmt.Errorf("--role must be one of view, edit, admin (got %q)", role)
			}
			selected, err := selectionFor(tenant, project)
			if err != nil {
				return err
			}
			c, err := o.tenantClient(cmd.Context(), selected.Tenant)
			if err != nil {
				return err
			}
			kind := tenancyv1alpha1.SubjectKindUser
			if asGroup {
				kind = tenancyv1alpha1.SubjectKindGroup
			}
			return upsertMembership(cmd.Context(), c, kind, args[0],
				tenancyv1alpha1.Role(role), selected.Tenant, selected.Project)
		},
	}
	o.addFlags(cmd)
	tenantFlag(cmd, &tenant)
	cmd.Flags().StringVar(&role, "role", string(tenancyv1alpha1.RoleView), "view, edit or admin.")
	cmd.Flags().StringVar(&project, "project", "",
		"Narrow the grant to one project. Defaults to the one `use` selected; "+
			"pass --project= to grant tenant-wide.")
	cmd.Flags().BoolVar(&asGroup, "group", false, "Treat SUBJECT as a group rather than a username.")
	return cmd
}

func newMembershipRemoveCommand() *cobra.Command {
	o := &adminOptions{}
	var (
		tenant  string
		project string
	)

	cmd := &cobra.Command{
		Use:     "remove SUBJECT",
		Aliases: []string{"revoke", "rm"},
		Short:   "Revoke a grant",
		Long: `Delete the Membership, and with it the RBAC the operator
materialized in the target workspaces.

--project has to match how the grant was made: a tenant-wide grant and a
project-scoped one are different objects.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			selected, err := selectionFor(tenant, project)
			if err != nil {
				return err
			}
			c, err := o.tenantClient(cmd.Context(), selected.Tenant)
			if err != nil {
				return err
			}
			name := membershipName(args[0], selected.Tenant, selected.Project)
			membership := &tenancyv1alpha1.Membership{ObjectMeta: metav1.ObjectMeta{Name: name}}
			if err := c.Delete(cmd.Context(), membership); err != nil {
				if apierrors.IsNotFound(err) {
					return fmt.Errorf("no such grant: %s (try `membership list`)", name)
				}
				return fmt.Errorf("delete membership: %w", err)
			}
			fmt.Printf("membership/%s revoked\n", name)
			return nil
		},
	}
	o.addFlags(cmd)
	tenantFlag(cmd, &tenant)
	cmd.Flags().StringVar(&project, "project", "", "Project of the grant, if it was project-scoped.")
	return cmd
}
