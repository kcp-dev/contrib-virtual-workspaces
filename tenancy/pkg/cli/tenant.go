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
	"time"

	"github.com/spf13/cobra"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kcp-dev/contrib-virtual-workspaces/tenancy/pkg/naming"
	tenancyv1alpha1 "github.com/kcp-dev/contrib-virtual-workspaces/tenancy/sdk/apis/tenancy/v1alpha1"
)

func newTenantCommand() *cobra.Command {
	o := &adminOptions{}
	cmd := &cobra.Command{
		Use:     "tenant",
		Aliases: []string{"tenants"},
		Short:   "Create, list and delete tenants",
	}
	o.addFlags(cmd)

	var (
		displayName string
		description string
		wait_       bool
	)

	create := &cobra.Command{
		Use:   "create NAME",
		Short: "Create a tenant and the workspace behind it",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := o.client()
			if err != nil {
				return err
			}
			name := args[0]
			if displayName == "" {
				displayName = name
			}

			tenant := &tenancyv1alpha1.Tenant{
				ObjectMeta: metav1.ObjectMeta{Name: name},
				Spec: tenancyv1alpha1.TenantSpec{
					DisplayName: displayName,
					Description: description,
				},
			}
			if err := c.Create(cmd.Context(), tenant); err != nil {
				if apierrors.IsAlreadyExists(err) {
					return fmt.Errorf("tenant %q already exists in %s", name, o.workspace)
				}
				return fmt.Errorf("create tenant: %w", err)
			}
			fmt.Printf("tenant/%s created in %s\n", name, o.workspace)

			if !wait_ {
				return nil
			}
			return waitForTenant(cmd.Context(), c, name)
		},
	}
	create.Flags().StringVar(&displayName, "display-name", "",
		"Human name of the tenant; seeds the workspace name. Defaults to NAME.")
	create.Flags().StringVar(&description, "description", "", "Free text for humans.")
	create.Flags().BoolVar(&wait_, "wait", true, "Wait until the tenant's workspace is ready.")

	list := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List tenants",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := o.client()
			if err != nil {
				return err
			}
			var tenants tenancyv1alpha1.TenantList
			if err := c.List(cmd.Context(), &tenants); err != nil {
				return fmt.Errorf("list tenants: %w", err)
			}
			if len(tenants.Items) == 0 {
				fmt.Printf("no tenants in %s\n", o.workspace)
				return nil
			}
			rows := make([][]string, 0, len(tenants.Items))
			for _, t := range tenants.Items {
				rows = append(rows, []string{
					t.Name, t.Spec.DisplayName, string(dashPhase(t.Status.Phase)),
					dash(t.Status.Workspace), dash(t.Status.WorkspaceCluster),
				})
			}
			table([]string{"NAME", "DISPLAY NAME", "PHASE", "WORKSPACE", "CLUSTER"}, rows)
			return nil
		},
	}

	del := &cobra.Command{
		Use:     "delete NAME",
		Aliases: []string{"rm"},
		Short:   "Delete a tenant and its workspace",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := o.client()
			if err != nil {
				return err
			}
			tenant := &tenancyv1alpha1.Tenant{ObjectMeta: metav1.ObjectMeta{Name: args[0]}}
			if err := c.Delete(cmd.Context(), tenant); err != nil {
				return fmt.Errorf("delete tenant: %w", err)
			}
			// Everything nested under the tenant's workspace goes with it,
			// which is worth saying out loud rather than discovering.
			fmt.Printf("tenant/%s deleting; its workspace and every project inside it go too\n", args[0])
			return nil
		},
	}

	cmd.AddCommand(create, list, del)
	return cmd
}

// waitForTenant blocks until the operator reports the tenant ready, or
// explains what it is stuck on.
func waitForTenant(ctx context.Context, c client.Client, name string) error {
	var last tenancyv1alpha1.Tenant
	err := wait.PollUntilContextTimeout(ctx, time.Second, 2*time.Minute, true, func(ctx context.Context) (bool, error) {
		if err := c.Get(ctx, client.ObjectKey{Name: name}, &last); err != nil {
			return false, nil
		}
		switch last.Status.Phase {
		case tenancyv1alpha1.PhaseReady:
			return true, nil
		case tenancyv1alpha1.PhaseError:
			return false, fmt.Errorf("tenant %q failed: %s", name, last.Status.Message)
		default:
			return false, nil
		}
	})
	if err != nil {
		if last.Status.Message != "" {
			return fmt.Errorf("tenant %q is not ready: %s (%w)", name, last.Status.Message, err)
		}
		return fmt.Errorf("tenant %q is not ready — is the operator running? (%w)", name, err)
	}
	fmt.Printf("tenant/%s ready: workspace %s (cluster %s)\n",
		name, last.Status.Workspace, last.Status.WorkspaceCluster)
	return nil
}

func dashPhase(p tenancyv1alpha1.Phase) tenancyv1alpha1.Phase {
	if p == "" {
		return "Pending"
	}
	return p
}

// membershipName derives a deterministic object name for a grant, so that
// granting the same access twice is a no-op rather than a duplicate.
func membershipName(subject, tenant, project string) string {
	parts := naming.Slugify(subject) + "-" + naming.Slugify(tenant)
	if project != "" {
		parts += "-" + naming.Slugify(project)
	}
	return parts
}
