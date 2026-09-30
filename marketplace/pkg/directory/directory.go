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

// Package directory is the in-memory index the marketplace virtual
// workspace answers from: Providers mirrored from the catalog tier,
// published APIExports mirrored from every provider workspace. It is read-heavy,
// written only by the mirroring controllers, and guarded by one mutex —
// the catalog is small and a browse must never block a sync.
package directory

import (
	"sort"
	"sync"

	marketplacev1alpha1 "github.com/kcp-dev/contrib-virtual-workspaces/marketplace/sdk/apis/marketplace/v1alpha1"
)

// Key identifies a mirrored object by the logical cluster it lives in and
// its name there.
type Key struct {
	Cluster string
	Name    string
}

// Provider is the directory's view of a catalog record.
type Provider struct {
	DisplayName      string
	Description      string
	WorkspacePath    string
	WorkspaceCluster string
	Ready            bool
}

// Offering is the directory's view of a published APIExport.
//
// There is no Provider field: which provider an offering belongs to is not
// something the offering records, it is which workspace the export lives
// in. Catalog resolves it by matching the export's logical cluster against
// the Provider records, so a provider cannot be misattributed by anything
// written in the provider's own workspace.
type Offering struct {
	DisplayName  string
	Description  string
	Export       string
	Revision     string
	Claims       []marketplacev1alpha1.OfferingClaim
	Ready        bool
	IdentityHash string
}

// Directory is safe for concurrent use.
type Directory struct {
	mu        sync.RWMutex
	ready     bool
	providers map[Key]Provider
	offerings map[Key]Offering
}

// New returns an empty directory.
func New() *Directory {
	return &Directory{
		providers: map[Key]Provider{},
		offerings: map[Key]Offering{},
	}
}

// SetReady marks the initial sync complete; Ready reports it.
func (d *Directory) SetReady() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.ready = true
}

func (d *Directory) Ready() bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.ready
}

// Counts reports how many objects the directory holds, for debugging.
func (d *Directory) Counts() (providers, offerings int) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return len(d.providers), len(d.offerings)
}

func (d *Directory) UpsertProvider(key Key, p Provider) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.providers[key] = p
}

func (d *Directory) RemoveProvider(key Key) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.providers, key)
}

func (d *Directory) UpsertOffering(key Key, o Offering) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.offerings[key] = o
}

func (d *Directory) RemoveOffering(key Key) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.offerings, key)
}

// ForgetCluster drops everything mirrored from one logical cluster — a
// workspace that unbound its export leaves nothing behind.
func (d *Directory) ForgetCluster(cluster string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for key := range d.providers {
		if key.Cluster == cluster {
			delete(d.providers, key)
		}
	}
	for key := range d.offerings {
		if key.Cluster == cluster {
			delete(d.offerings, key)
		}
	}
}

// Entry is one catalog listing: a published export joined with the
// Provider record that owns its workspace.
type Entry struct {
	Provider string
	Offering string
	// WorkspacePath is the canonical path of the provider's workspace —
	// what the APIBinding a consumer authors references as
	// spec.reference.export.path.
	WorkspacePath string
	Detail        Offering
}

// Name is the entry's stable listing name, "<provider>.<export>".
func (e Entry) Name() string {
	return e.Provider + "." + e.Offering
}

// Catalog lists every offering whose workspace a Provider record names,
// sorted by name for stable output. An offering published from a workspace
// no record names is invisible — publishing into the void is not
// publishing, and it is also the case an unregistered workspace would use
// to put itself on the catalog by annotating its own export.
func (d *Directory) Catalog() []Entry {
	d.mu.RLock()
	defer d.mu.RUnlock()

	// The join is workspace → provider: the record says which logical
	// cluster it owns, and the offering is in one.
	providerByCluster := map[string]struct {
		name   string
		record Provider
	}{}
	for key, p := range d.providers {
		if p.WorkspaceCluster == "" {
			continue
		}
		providerByCluster[p.WorkspaceCluster] = struct {
			name   string
			record Provider
		}{key.Name, p}
	}

	var out []Entry
	for key, offering := range d.offerings {
		owner, known := providerByCluster[key.Cluster]
		if !known {
			continue
		}
		out = append(out, Entry{
			Provider:      owner.name,
			Offering:      key.Name,
			WorkspacePath: owner.record.WorkspacePath,
			Detail:        offering,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out
}

// Lookup returns one catalog entry by its listing name.
func (d *Directory) Lookup(name string) (Entry, bool) {
	for _, e := range d.Catalog() {
		if e.Name() == name {
			return e, true
		}
	}
	return Entry{}, false
}
