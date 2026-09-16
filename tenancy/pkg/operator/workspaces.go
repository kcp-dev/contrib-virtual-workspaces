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

package operator

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	kcptenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"

	tenancyv1alpha1 "github.com/kcp-dev/contrib-virtual-workspaces/tenancy/sdk/apis/tenancy/v1alpha1"
)

// ownerUIDLabel ties a provisioned Workspace to the Tenant or Project that
// asked for it. It is how a reconcile recognizes its own workspace among
// naming candidates, and how a collision with someone else's workspace is
// told apart from an earlier run of the same reconcile.
const ownerUIDLabel = tenancyv1alpha1.GroupName + "/owner-uid"

// provisioned describes the workspace a tenant or project ended up with.
type provisioned struct {
	// Name of the Workspace object in its parent workspace.
	Name string
	// Ready is true once kcp reports the workspace ready and has assigned
	// its cluster.
	Ready bool
	// Cluster is the logical cluster backing the workspace, empty until
	// scheduling.
	Cluster string
	// URL is the workspace's direct address, empty until scheduling.
	URL string
}

// ensureWorkspace makes sure a workspace for the owner exists under the
// parent addressed by c, trying the strategy's candidates in order. The
// call is idempotent: a workspace already labeled with the owner's UID is
// adopted no matter which candidate produced its name.
func ensureWorkspace(ctx context.Context, c client.Client, displayName, ownerUID string, candidates []string) (provisioned, error) {
	if len(candidates) == 0 {
		return provisioned{}, fmt.Errorf("naming strategy proposed no candidates")
	}

	for _, name := range candidates {
		var ws kcptenancyv1alpha1.Workspace
		err := c.Get(ctx, types.NamespacedName{Name: name}, &ws)
		switch {
		case apierrors.IsNotFound(err):
			create := kcptenancyv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:   name,
					Labels: map[string]string{ownerUIDLabel: ownerUID},
				},
			}
			if err := c.Create(ctx, &create); err != nil {
				if apierrors.IsAlreadyExists(err) {
					// Lost a race for this name; look at the next candidate.
					continue
				}
				return provisioned{}, fmt.Errorf("create workspace %q: %w", name, err)
			}
			return provisioned{Name: name}, nil

		case err != nil:
			return provisioned{}, fmt.Errorf("get workspace %q: %w", name, err)

		case ws.Labels[ownerUIDLabel] == ownerUID:
			return fromWorkspace(&ws), nil

		default:
			// The name is taken by a workspace that is not ours — for the
			// slug strategy that is another tenant with the same display
			// name. Fall through to the next, UID-derived candidate.
			continue
		}
	}

	return provisioned{}, fmt.Errorf("every candidate name for %q is taken by a workspace with a different owner", displayName)
}

func fromWorkspace(ws *kcptenancyv1alpha1.Workspace) provisioned {
	return provisioned{
		Name:    ws.Name,
		Ready:   ws.Status.Phase == corev1alpha1.LogicalClusterPhaseReady && ws.Spec.Cluster != "",
		Cluster: ws.Spec.Cluster,
		URL:     ws.Spec.URL,
	}
}

// deleteWorkspace removes the owner's workspace if it still exists and is
// still the owner's. Returns true once nothing is left to wait for.
func deleteWorkspace(ctx context.Context, c client.Client, name, ownerUID string) (bool, error) {
	if name == "" {
		return true, nil
	}
	var ws kcptenancyv1alpha1.Workspace
	err := c.Get(ctx, types.NamespacedName{Name: name}, &ws)
	if apierrors.IsNotFound(err) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("get workspace %q: %w", name, err)
	}
	if ws.Labels[ownerUIDLabel] != ownerUID {
		// Not ours (anymore); nothing to clean up.
		return true, nil
	}
	if ws.DeletionTimestamp == nil {
		if err := c.Delete(ctx, &ws); err != nil && !apierrors.IsNotFound(err) {
			return false, fmt.Errorf("delete workspace %q: %w", name, err)
		}
	}
	// Deletion in flight; report done only when the object is gone so the
	// finalizer holds until kcp has really removed the workspace.
	return false, nil
}
