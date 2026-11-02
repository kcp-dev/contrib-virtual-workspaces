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

package provider

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
)

// Clusters hands out a client for one logical cluster in the fleet.
//
// An APIExport's virtual workspace runs per shard: the endpoint slice
// lists one URL for each shard serving the export, and each of those URLs
// serves only the clusters that live on that shard. So there is no single
// URL a write can be addressed to. Asking a shard about a cluster it does
// not host is refused as a plain Forbidden — that shard has no APIBinding
// for the cluster, and the virtual workspace's authorizer anonymizes the
// reason, so the answer is indistinguishable from the caller having no
// standing at all.
//
// The read side already solves this: the provider watches every URL the
// slice publishes and knows which endpoint each cluster was discovered
// through. Clusters exposes that, so a write goes to the shard that
// actually holds the workspace rather than to whichever URL happened to
// be listed first.
type Clusters struct {
	// ready is closed once Run has built the managers. Everything below is
	// written before the close and only read after it, which is what makes
	// the handoff to request handlers safe without a lock.
	ready    chan struct{}
	platform mcmanager.Manager
	tenancy  mcmanager.Manager
}

// NewClusters returns a Clusters that Run fills in.
func NewClusters() *Clusters {
	return &Clusters{ready: make(chan struct{})}
}

func (c *Clusters) set(platform, tenancy mcmanager.Manager) {
	c.platform, c.tenancy = platform, tenancy
	close(c.ready)
}

// Tenancy returns a client for a tenant workspace, which is where Projects
// and Memberships live.
func (c *Clusters) Tenancy(ctx context.Context, cluster string) (client.Client, error) {
	if err := c.wait(ctx); err != nil {
		return nil, err
	}
	return clientFor(ctx, c.tenancy, cluster)
}

// Platform returns a client for a workspace in the platform tier, which is
// where Tenant records live.
func (c *Clusters) Platform(ctx context.Context, cluster string) (client.Client, error) {
	if err := c.wait(ctx); err != nil {
		return nil, err
	}
	return clientFor(ctx, c.platform, cluster)
}

// wait blocks until the managers exist. In practice they always do by the
// time a write is attempted: every write is authorized against the
// directory first, and the directory is not ready until both managers have
// synced.
func (c *Clusters) wait(ctx context.Context) error {
	select {
	case <-c.ready:
		return nil
	case <-ctx.Done():
		return apierrors.NewServiceUnavailable("the tenancy directory is still starting")
	}
}

func clientFor(ctx context.Context, mgr mcmanager.Manager, cluster string) (client.Client, error) {
	if cluster == "" {
		return nil, fmt.Errorf("no logical cluster given")
	}
	cl, err := mgr.GetCluster(ctx, multicluster.ClusterName(cluster))
	if err != nil {
		// A cluster the provider has not discovered is one no shard has
		// reported binding the export — usually a workspace that is still
		// initializing, which is worth retrying rather than reporting as a
		// refusal.
		return nil, apierrors.NewServiceUnavailable(fmt.Sprintf(
			"workspace %s has not joined the fleet yet: %v", cluster, err))
	}
	return cl.GetClient(), nil
}
