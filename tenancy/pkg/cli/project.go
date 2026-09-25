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

	tenancyv1alpha1 "github.com/kcp-dev/contrib-virtual-workspaces/tenancy/sdk/apis/tenancy/v1alpha1"
)

func newProjectCommand() *cobra.Command {
	o := &adminOptions{}
	cmd := &cobra.Command{
		Use:     "project",
		Aliases: []string{"projects"},
		Short:   "Create, list and delete projects inside a tenant",
	}
	o.addFlags(cmd)

	var (
		tenant      string
		displayName string
		description string
		wait_       bool
	)

	create := &cobra.Command{
		Use:   "create NAME",
		Short: "Create a project and the workspace behind it",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if tenant == "" {
				return fmt.Errorf("--tenant is required")
			}
			c, err := o.tenantClient(cmd.Context(), tenant)
			if err != nil {
				return err
			}
			name := args[0]
			if displayName == "" {
				displayName = name
			}

			project := &tenancyv1alpha1.Project{
				ObjectMeta: metav1.ObjectMeta{Name: name},
				Spec: tenancyv1alpha1.ProjectSpec{
					Tenant:      tenant,
					DisplayName: displayName,
					Description: description,
				},
			}
			if err := c.Create(cmd.Context(), project); err != nil {
				if apierrors.IsAlreadyExists(err) {
					return fmt.Errorf("project %q already exists in %s", name, o.workspace)
				}
				return fmt.Errorf("create project: %w", err)
			}
			fmt.Printf("project/%s created in tenant %s\n", name, tenant)

			if !wait_ {
				return nil
			}
			return waitForProject(cmd.Context(), c, name)
		},
	}
	create.Flags().StringVar(&tenant, "tenant", "", "Tenant this project belongs to (required).")
	create.Flags().StringVar(&displayName, "display-name", "",
		"Human name of the project; seeds the workspace name. Defaults to NAME.")
	create.Flags().StringVar(&description, "description", "", "Free text for humans.")
	create.Flags().BoolVar(&wait_, "wait", true, "Wait until the project's workspace is ready.")

	var listTenant string
	list := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List projects",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if listTenant == "" {
				return fmt.Errorf("--tenant is required: projects live inside their tenant's workspace")
			}
			c, err := o.tenantClient(cmd.Context(), listTenant)
			if err != nil {
				return err
			}
			var projects tenancyv1alpha1.ProjectList
			if err := c.List(cmd.Context(), &projects); err != nil {
				return fmt.Errorf("list projects: %w", err)
			}
			rows := make([][]string, 0, len(projects.Items))
			for _, p := range projects.Items {
				rows = append(rows, []string{
					p.Name, listTenant, p.Spec.DisplayName,
					string(dashPhase(p.Status.Phase)), dash(p.Status.WorkspaceCluster),
				})
			}
			if len(rows) == 0 {
				fmt.Printf("no projects in tenant %s\n", listTenant)
				return nil
			}
			table([]string{"NAME", "TENANT", "DISPLAY NAME", "PHASE", "CLUSTER"}, rows)
			return nil
		},
	}
	list.Flags().StringVar(&listTenant, "tenant", "", "Tenant whose projects to list (required).")

	del := &cobra.Command{
		Use:     "delete NAME",
		Aliases: []string{"rm"},
		Short:   "Delete a project and its workspace",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if tenant == "" {
				return fmt.Errorf("--tenant is required: projects live inside their tenant's workspace")
			}
			c, err := o.tenantClient(cmd.Context(), tenant)
			if err != nil {
				return err
			}
			project := &tenancyv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: args[0]}}
			if err := c.Delete(cmd.Context(), project); err != nil {
				return fmt.Errorf("delete project: %w", err)
			}
			fmt.Printf("project/%s deleting; its workspace goes too\n", args[0])
			return nil
		},
	}

	del.Flags().StringVar(&tenant, "tenant", "", "Tenant the project belongs to (required).")

	cmd.AddCommand(create, list, del)
	return cmd
}

func waitForProject(ctx context.Context, c client.Client, name string) error {
	var last tenancyv1alpha1.Project
	err := wait.PollUntilContextTimeout(ctx, time.Second, 2*time.Minute, true, func(ctx context.Context) (bool, error) {
		if err := c.Get(ctx, client.ObjectKey{Name: name}, &last); err != nil {
			return false, nil
		}
		switch last.Status.Phase {
		case tenancyv1alpha1.PhaseReady:
			return true, nil
		case tenancyv1alpha1.PhaseError:
			return false, fmt.Errorf("project %q failed: %s", name, last.Status.Message)
		default:
			return false, nil
		}
	})
	if err != nil {
		if last.Status.Message != "" {
			return fmt.Errorf("project %q is not ready: %s (%w)", name, last.Status.Message, err)
		}
		return fmt.Errorf("project %q is not ready — is the operator running, and is its tenant ready? (%w)", name, err)
	}
	fmt.Printf("project/%s ready: workspace %s (cluster %s)\n",
		name, last.Status.Workspace, last.Status.WorkspaceCluster)
	return nil
}
