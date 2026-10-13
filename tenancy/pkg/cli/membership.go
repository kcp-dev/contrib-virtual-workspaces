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
	"sigs.k8s.io/controller-runtime/pkg/client"

	tenancyv1alpha1 "github.com/kcp-dev/contrib-virtual-workspaces/tenancy/sdk/apis/tenancy/v1alpha1"
)

func newGrantCommand() *cobra.Command {
	o := &adminOptions{}
	var (
		role    string
		tenant  string
		project string
		asGroup bool
	)

	cmd := &cobra.Command{
		Use:   "grant SUBJECT",
		Short: "Grant a user or group a role in a tenant or project",
		Long: `Grant access by creating a Membership.

SUBJECT is matched verbatim against the identity kcp authenticates: the
CommonName of a client certificate, or the username an OIDC token maps to.
With --group it is matched against the caller's groups instead (a
certificate Organization, or a groups claim).

There is no user registry to add anyone to — issuing the credential is the
identity provider's job, and this only records what that identity may do.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if tenant == "" {
				return fmt.Errorf("--tenant is required")
			}
			switch tenancyv1alpha1.Role(role) {
			case tenancyv1alpha1.RoleView, tenancyv1alpha1.RoleEdit, tenancyv1alpha1.RoleAdmin:
			default:
				return fmt.Errorf("--role must be one of view, edit, admin (got %q)", role)
			}

			c, err := o.tenantClient(cmd.Context(), tenant)
			if err != nil {
				return err
			}

			kind := tenancyv1alpha1.SubjectKindUser
			if asGroup {
				kind = tenancyv1alpha1.SubjectKindGroup
			}
			subject := args[0]
			name := membershipName(subject, tenant, project)

			membership := &tenancyv1alpha1.Membership{
				ObjectMeta: metav1.ObjectMeta{Name: name},
				Spec: tenancyv1alpha1.MembershipSpec{
					Subject: tenancyv1alpha1.Subject{Kind: kind, Name: subject},
					Role:    tenancyv1alpha1.Role(role),
					Tenant:  tenant,
					Project: project,
				},
			}

			err = c.Create(cmd.Context(), membership)
			if apierrors.IsAlreadyExists(err) {
				// The name is derived from (subject, tenant, project), so
				// an existing object is the same grant: re-granting is a
				// role change, not a duplicate.
				var existing tenancyv1alpha1.Membership
				if err := c.Get(cmd.Context(), client.ObjectKey{Name: name}, &existing); err != nil {
					return fmt.Errorf("get existing membership: %w", err)
				}
				if existing.Spec.Role == tenancyv1alpha1.Role(role) {
					fmt.Printf("membership/%s already grants %s\n", name, role)
					return nil
				}
				previous := existing.Spec.Role
				existing.Spec.Role = tenancyv1alpha1.Role(role)
				if err := c.Update(cmd.Context(), &existing); err != nil {
					return fmt.Errorf("update membership: %w", err)
				}
				fmt.Printf("membership/%s changed from %s to %s\n", name, previous, role)
				return nil
			}
			if err != nil {
				return fmt.Errorf("create membership: %w", err)
			}

			scope := "every project of tenant " + tenant
			if project != "" {
				scope = "project " + project + " of tenant " + tenant
			}
			fmt.Printf("membership/%s created: %s %q is %s in %s\n", name, kind, subject, role, scope)
			return nil
		},
	}
	o.addFlags(cmd)
	cmd.Flags().StringVar(&role, "role", string(tenancyv1alpha1.RoleView), "view, edit or admin.")
	cmd.Flags().StringVar(&tenant, "tenant", "", "Tenant the grant applies to (required).")
	cmd.Flags().StringVar(&project, "project", "",
		"Narrow the grant to one project of the tenant. Empty grants it tenant-wide, "+
			"which reaches every project.")
	cmd.Flags().BoolVar(&asGroup, "group", false, "Treat SUBJECT as a group rather than a username.")

	return cmd
}

func newRevokeCommand() *cobra.Command {
	o := &adminOptions{}
	var (
		tenant  string
		project string
		list    bool
	)

	cmd := &cobra.Command{
		Use:   "revoke [SUBJECT]",
		Short: "Revoke a grant, or list the grants in this workspace",
		Long: `Revoke deletes the Membership, and with it the RBAC the operator
materialized in the target workspace.

With --list it prints every grant instead of removing one.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if tenant == "" {
				return fmt.Errorf("--tenant is required: grants live inside their tenant's workspace")
			}
			c, err := o.tenantClient(cmd.Context(), tenant)
			if err != nil {
				return err
			}

			if list || len(args) == 0 {
				var memberships tenancyv1alpha1.MembershipList
				if err := c.List(cmd.Context(), &memberships); err != nil {
					return fmt.Errorf("list memberships: %w", err)
				}
				if len(memberships.Items) == 0 {
					fmt.Printf("no grants in tenant %s\n", tenant)
					return nil
				}
				rows := make([][]string, 0, len(memberships.Items))
				for _, m := range memberships.Items {
					rows = append(rows, []string{
						m.Name, string(m.Spec.Subject.Kind), m.Spec.Subject.Name,
						string(m.Spec.Role), m.Spec.Tenant, dash(m.Spec.Project),
						string(dashPhase(m.Status.Phase)),
					})
				}
				table([]string{"NAME", "KIND", "SUBJECT", "ROLE", "TENANT", "PROJECT", "PHASE"}, rows)
				return nil
			}

			name := membershipName(args[0], tenant, project)
			membership := &tenancyv1alpha1.Membership{ObjectMeta: metav1.ObjectMeta{Name: name}}
			if err := c.Delete(cmd.Context(), membership); err != nil {
				if apierrors.IsNotFound(err) {
					return fmt.Errorf("no such grant: %s (try `tenancyctl revoke --list`)", name)
				}
				return fmt.Errorf("delete membership: %w", err)
			}
			fmt.Printf("membership/%s revoked\n", name)
			return nil
		},
	}
	o.addFlags(cmd)
	cmd.Flags().StringVar(&tenant, "tenant", "", "Tenant whose grants to list or revoke (required).")
	cmd.Flags().StringVar(&project, "project", "", "Project of the grant, if it was project-scoped.")
	cmd.Flags().BoolVar(&list, "list", false, "List grants instead of revoking one.")

	return cmd
}
