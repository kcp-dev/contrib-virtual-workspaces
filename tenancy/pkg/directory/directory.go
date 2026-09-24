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

// Package directory keeps the tenancy virtual workspace's in-memory answer
// state: every Tenant, Project and Membership across all organization
// workspaces, indexed for the one question the VW answers — "what does
// this identity belong to?".
//
// The directory is written by informer-driven controllers and read on
// every SelfTenancyReview, so writes take the lock briefly per object and
// reads assemble the answer under a read lock. Objects are keyed by
// (logical cluster, name): names only mean something inside their
// organization workspace.
package directory

import (
	"sort"
	"sync"

	tenancyv1alpha1 "github.com/kcp-dev/contrib-virtual-workspaces/tenancy/sdk/apis/tenancy/v1alpha1"
)

// Key addresses one object in one organization workspace.
type Key struct {
	// Cluster is the logical cluster of the organization workspace the
	// object lives in.
	Cluster string
	// Name is the object's name.
	Name string
}

// Tenant is the directory's projection of a Tenant object.
type Tenant struct {
	DisplayName string
	// WorkspaceCluster is the logical cluster of the tenant's provisioned
	// workspace; empty until the operator reports it.
	WorkspaceCluster string
}

// Project is the directory's projection of a Project object.
type Project struct {
	// Tenant is the name of the owning Tenant, in the same cluster.
	Tenant           string
	DisplayName      string
	WorkspaceCluster string
}

// Membership is the directory's projection of a Membership object.
type Membership struct {
	Subject tenancyv1alpha1.Subject
	Role    string
	// Tenant is the name of the Tenant the grant applies to, in the same
	// cluster as the Membership.
	Tenant string
	// Project is empty for a tenant-wide grant.
	Project string
}

// Directory is safe for concurrent use.
//
// The three kinds arrive from two different tiers: Tenants from the
// platform workspace, Projects and Memberships from inside each tenant's
// own workspace. They are joined on the tenant's workspace cluster — a
// Membership belongs to the tenant whose workspace it lives in — so
// nothing here relies on a name matching across a workspace boundary.
type Directory struct {
	mu sync.RWMutex

	tenants     map[Key]Tenant
	projects    map[Key]Project
	memberships map[Key]Membership

	// byWorkspace resolves a tenant's workspace cluster back to the Tenant
	// object that asked for it.
	byWorkspace map[string]Key

	ready bool
}

// New returns an empty directory.
func New() *Directory {
	return &Directory{
		tenants:     map[Key]Tenant{},
		projects:    map[Key]Project{},
		memberships: map[Key]Membership{},
		byWorkspace: map[string]Key{},
	}
}

// SetReady marks the initial sync complete. Before this, reviews are
// refused rather than answered partially.
func (d *Directory) SetReady() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.ready = true
}

// Ready reports whether the initial sync completed.
func (d *Directory) Ready() bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.ready
}

// UpsertTenant records a tenant.
func (d *Directory) UpsertTenant(k Key, t Tenant) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if old, ok := d.tenants[k]; ok && old.WorkspaceCluster != "" && old.WorkspaceCluster != t.WorkspaceCluster {
		delete(d.byWorkspace, old.WorkspaceCluster)
	}
	d.tenants[k] = t
	if t.WorkspaceCluster != "" {
		d.byWorkspace[t.WorkspaceCluster] = k
	}
}

// RemoveTenant forgets a tenant.
func (d *Directory) RemoveTenant(k Key) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if old, ok := d.tenants[k]; ok && old.WorkspaceCluster != "" {
		delete(d.byWorkspace, old.WorkspaceCluster)
	}
	delete(d.tenants, k)
}

// UpsertProject records a project.
func (d *Directory) UpsertProject(k Key, p Project) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.projects[k] = p
}

// RemoveProject forgets a project.
func (d *Directory) RemoveProject(k Key) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.projects, k)
}

// UpsertMembership records a membership.
func (d *Directory) UpsertMembership(k Key, m Membership) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.memberships[k] = m
}

// RemoveMembership forgets a membership.
func (d *Directory) RemoveMembership(k Key) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.memberships, k)
}

// ForgetCluster drops everything recorded for one organization workspace,
// for when a cluster leaves the fleet.
func (d *Directory) ForgetCluster(cluster string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for k, t := range d.tenants {
		if k.Cluster == cluster {
			if t.WorkspaceCluster != "" {
				delete(d.byWorkspace, t.WorkspaceCluster)
			}
			delete(d.tenants, k)
		}
	}
	for k := range d.projects {
		if k.Cluster == cluster {
			delete(d.projects, k)
		}
	}
	for k := range d.memberships {
		if k.Cluster == cluster {
			delete(d.memberships, k)
		}
	}
}

// Counts returns how many objects the directory holds, for debug output.
func (d *Directory) Counts() (tenants, projects, memberships int) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return len(d.tenants), len(d.projects), len(d.memberships)
}

// ReviewFor assembles the SelfTenancyReview answer for one identity.
// endpointFor turns a workspace's logical cluster name into the URL the
// caller can reach it at; it is only called for non-empty clusters.
//
// A caller reaches a tenant if any membership for that tenant matches the
// identity — tenant-wide or on any project. Tenant-wide roles carry into
// every project of the tenant; project-scoped roles reach only their
// project. Output is sorted by name at every level so answers are stable.
func (d *Directory) ReviewFor(username string, groups []string, endpointFor func(cluster string) string) []tenancyv1alpha1.TenantClaim {
	groupSet := make(map[string]struct{}, len(groups))
	for _, g := range groups {
		groupSet[g] = struct{}{}
	}

	d.mu.RLock()
	defer d.mu.RUnlock()

	// Roles are collected per tenant WORKSPACE cluster, because that is
	// the cluster a Membership lives in. The Tenant object itself is in the
	// platform workspace and is resolved at the end.
	tenantRoles := map[string]map[string]struct{}{}
	projectRoles := map[string]map[string]map[string]struct{}{}

	for k, m := range d.memberships {
		switch m.Subject.Kind {
		case tenancyv1alpha1.SubjectKindUser:
			if m.Subject.Name != username {
				continue
			}
		case tenancyv1alpha1.SubjectKindGroup:
			if _, ok := groupSet[m.Subject.Name]; !ok {
				continue
			}
		default:
			continue
		}

		ws := k.Cluster
		if m.Project == "" {
			if tenantRoles[ws] == nil {
				tenantRoles[ws] = map[string]struct{}{}
			}
			tenantRoles[ws][m.Role] = struct{}{}
			continue
		}
		if projectRoles[ws] == nil {
			projectRoles[ws] = map[string]map[string]struct{}{}
		}
		if projectRoles[ws][m.Project] == nil {
			projectRoles[ws][m.Project] = map[string]struct{}{}
		}
		projectRoles[ws][m.Project][m.Role] = struct{}{}
	}

	seen := map[string]struct{}{}
	for ws := range tenantRoles {
		seen[ws] = struct{}{}
	}
	for ws := range projectRoles {
		seen[ws] = struct{}{}
	}

	claims := make([]tenancyv1alpha1.TenantClaim, 0, len(seen))
	for ws := range seen {
		tenantKey, ok := d.byWorkspace[ws]
		if !ok {
			// A grant in a workspace whose Tenant the directory has not
			// seen yet (or that is gone); nothing useful to claim.
			continue
		}
		tenant := d.tenants[tenantKey]

		claim := tenancyv1alpha1.TenantClaim{
			Name:        tenantKey.Name,
			DisplayName: tenant.DisplayName,
			Cluster:     tenant.WorkspaceCluster,
			Roles:       sortedRoles(tenantRoles[ws]),
		}
		if claim.Cluster != "" {
			claim.Endpoint = endpointFor(claim.Cluster)
		}

		wide := tenantRoles[ws]
		scoped := projectRoles[ws]
		for pk, project := range d.projects {
			// Projects live in the tenant's workspace, alongside the
			// Memberships that grant them.
			if pk.Cluster != ws {
				continue
			}
			roles := unionRoles(wide, scoped[pk.Name])
			if len(roles) == 0 {
				continue
			}
			pc := tenancyv1alpha1.ProjectClaim{
				Name:        pk.Name,
				DisplayName: project.DisplayName,
				Cluster:     project.WorkspaceCluster,
				Roles:       roles,
			}
			if pc.Cluster != "" {
				pc.Endpoint = endpointFor(pc.Cluster)
			}
			claim.Projects = append(claim.Projects, pc)
		}
		sort.Slice(claim.Projects, func(i, j int) bool { return claim.Projects[i].Name < claim.Projects[j].Name })

		claims = append(claims, claim)
	}

	sort.Slice(claims, func(i, j int) bool {
		if claims[i].Name != claims[j].Name {
			return claims[i].Name < claims[j].Name
		}
		return claims[i].Cluster < claims[j].Cluster
	})
	return claims
}

func sortedRoles(set map[string]struct{}) []string {
	if len(set) == 0 {
		return nil
	}
	out := make([]string, 0, len(set))
	for r := range set {
		out = append(out, r)
	}
	sort.Strings(out)
	return out
}

func unionRoles(a, b map[string]struct{}) []string {
	union := make(map[string]struct{}, len(a)+len(b))
	for r := range a {
		union[r] = struct{}{}
	}
	for r := range b {
		union[r] = struct{}{}
	}
	return sortedRoles(union)
}
