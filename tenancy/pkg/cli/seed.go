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

// NewSeedCommand exposes seed for the tenancy binary, so a development
// environment (Tilt, a demo) can converge on baseline data with the
// component's own tooling and nothing else.
func NewSeedCommand() *cobra.Command { return newSeedCommand() }

// newSeedCommand builds `seed`: one idempotent call that converges on a
// tenant, optionally a project, and a set of grants. Everything it does
// is if-not-present or an in-place role change, so calling it again — on
// every environment start, say — is always safe. The identities are
// arguments, not defaults: which users exist is the environment's
// business, not the component's.
func newSeedCommand() *cobra.Command {
	o := &adminOptions{}
	var (
		tenant      string
		displayName string
		project     string
		admins      []string
		views       []string
		wait_       bool
	)

	cmd := &cobra.Command{
		Use:   "seed",
		Short: "Idempotently converge on a tenant, a project and grants",
		Long: `Seed baseline tenancy data for an environment, idempotently.

Creates the tenant and project when missing (and waits for their
workspaces), then upserts the grants: --admin subjects become tenant-wide
admins, --view subjects get view on the project (tenant-wide when no
project is given). Safe to run on every environment start.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if tenant == "" {
				return fmt.Errorf("--tenant is required")
			}
			ctx := cmd.Context()

			platform, err := o.client()
			if err != nil {
				return err
			}
			if displayName == "" {
				displayName = tenant
			}
			t := &tenancyv1alpha1.Tenant{
				ObjectMeta: metav1.ObjectMeta{Name: tenant},
				Spec: tenancyv1alpha1.TenantSpec{
					DisplayName: displayName,
					// The first --admin also seeds the tenant, so the
					// grant exists the moment the workspace does rather
					// than after the projects below it.
					FirstAdmin: firstOf(admins),
				},
			}
			switch err := platform.Create(ctx, t); {
			case err == nil:
				fmt.Printf("tenant/%s created in %s\n", tenant, o.workspace)
			case apierrors.IsAlreadyExists(err):
				fmt.Printf("tenant/%s already exists in %s\n", tenant, o.workspace)
			default:
				return fmt.Errorf("create tenant: %w", err)
			}
			if wait_ {
				if err := waitForTenant(ctx, platform, tenant); err != nil {
					return err
				}
			}

			tc, err := o.tenantClient(ctx, tenant)
			if err != nil {
				return err
			}

			if project != "" {
				p := &tenancyv1alpha1.Project{
					ObjectMeta: metav1.ObjectMeta{Name: project},
					Spec: tenancyv1alpha1.ProjectSpec{
						Tenant:      tenant,
						DisplayName: project,
					},
				}
				switch err := tc.Create(ctx, p); {
				case err == nil:
					fmt.Printf("project/%s created in tenant %s\n", project, tenant)
				case apierrors.IsAlreadyExists(err):
					fmt.Printf("project/%s already exists in tenant %s\n", project, tenant)
				default:
					return fmt.Errorf("create project: %w", err)
				}
				if wait_ {
					if err := waitForProject(ctx, tc, project); err != nil {
						return err
					}
				}
			}

			for _, subject := range admins {
				if err := upsertMembership(ctx, tc, tenancyv1alpha1.SubjectKindUser, subject,
					tenancyv1alpha1.RoleAdmin, tenant, ""); err != nil {
					return err
				}
			}
			for _, subject := range views {
				if err := upsertMembership(ctx, tc, tenancyv1alpha1.SubjectKindUser, subject,
					tenancyv1alpha1.RoleView, tenant, project); err != nil {
					return err
				}
			}

			fmt.Printf("seeded tenant %s\n", tenant)
			return nil
		},
	}
	o.addFlags(cmd)
	cmd.Flags().StringVar(&tenant, "tenant", "", "Tenant to converge on (required).")
	cmd.Flags().StringVar(&displayName, "display-name", "",
		"Human name of the tenant; seeds the workspace name. Defaults to the tenant name.")
	cmd.Flags().StringVar(&project, "project", "", "Project to converge on inside the tenant.")
	cmd.Flags().StringArrayVar(&admins, "admin", nil,
		"Subject to grant tenant-wide admin (repeatable).")
	cmd.Flags().StringArrayVar(&views, "view", nil,
		"Subject to grant view on the project, or tenant-wide without --project (repeatable).")
	cmd.Flags().BoolVar(&wait_, "wait", true, "Wait until the workspaces are ready.")

	return cmd
}

// firstOf returns the first element, or "".
func firstOf(ss []string) string {
	if len(ss) == 0 {
		return ""
	}
	return ss[0]
}
