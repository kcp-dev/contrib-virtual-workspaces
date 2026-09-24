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

	marketplacev1alpha1 "github.com/kcp-dev/contrib-virtual-workspaces/marketplace/sdk/apis/marketplace/v1alpha1"
)

// ownerUIDLabel ties a provisioned Workspace to the Provider that asked
// for it, so a reconcile recognizes its own workspace and a name collision
// with someone else's is told apart from an earlier run.
const ownerUIDLabel = marketplacev1alpha1.GroupName + "/owner-uid"

// provisioned describes the workspace a provider ended up with.
type provisioned struct {
	Name    string
	Ready   bool
	Cluster string
	URL     string
}

// ensureWorkspace makes sure a workspace named name exists under the
// parent addressed by c, owned by ownerUID, with the given type. A
// Provider's name is already a DNS label, so unlike tenancy there is no
// naming strategy: the workspace is named after the record, and a name
// held by a workspace with a different owner is an error, not a retry.
func ensureWorkspace(ctx context.Context, c client.Client, name, ownerUID, wsType, typePath string) (provisioned, error) {
	var ws kcptenancyv1alpha1.Workspace
	err := c.Get(ctx, types.NamespacedName{Name: name}, &ws)
	switch {
	case apierrors.IsNotFound(err):
		create := kcptenancyv1alpha1.Workspace{
			ObjectMeta: metav1.ObjectMeta{
				Name:   name,
				Labels: map[string]string{ownerUIDLabel: ownerUID},
			},
			Spec: kcptenancyv1alpha1.WorkspaceSpec{
				Type: &kcptenancyv1alpha1.WorkspaceTypeReference{
					Name: kcptenancyv1alpha1.WorkspaceTypeName(wsType),
					Path: typePath,
				},
			},
		}
		if err := c.Create(ctx, &create); err != nil && !apierrors.IsAlreadyExists(err) {
			return provisioned{}, fmt.Errorf("create workspace %q: %w", name, err)
		}
		return provisioned{Name: name}, nil

	case err != nil:
		return provisioned{}, fmt.Errorf("get workspace %q: %w", name, err)

	case ws.Labels[ownerUIDLabel] == ownerUID:
		return provisioned{
			Name:    ws.Name,
			Ready:   ws.Status.Phase == corev1alpha1.LogicalClusterPhaseReady && ws.Spec.Cluster != "",
			Cluster: ws.Spec.Cluster,
			URL:     ws.Spec.URL,
		}, nil

	default:
		return provisioned{}, fmt.Errorf("workspace %q exists but belongs to something else", name)
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
		return true, nil
	}
	if ws.DeletionTimestamp == nil {
		if err := c.Delete(ctx, &ws); err != nil && !apierrors.IsNotFound(err) {
			return false, fmt.Errorf("delete workspace %q: %w", name, err)
		}
	}
	return false, nil
}
