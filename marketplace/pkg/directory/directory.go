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
// published APIExports mirrored from every producer workspace. It is read-heavy,
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

// Producer is the directory's view of a catalog record.
type Producer struct {
	DisplayName      string
	Description      string
	WorkspacePath    string
	WorkspaceCluster string
	Ready            bool
}

// Offering is the directory's view of a published APIExport.
//
// There is no Producer field: which producer an offering belongs to is not
// something the offering records, it is which workspace the export lives
// in. Catalog resolves it by matching the export's logical cluster against
// the Producer records, so a producer cannot be misattributed by anything
// written in the producer's own workspace.
type Offering struct {
	DisplayName  string
	Description  string
	Export       string
	Revision     string
	Claims       []marketplacev1alpha1.OfferingClaim
	Ready        bool
	IdentityHash string
	DocsURL      string
	// InstructionsRef names a ConfigMap in the offering's own cluster; the
	// content is mirrored separately and joined at read time, so an edit
	// to the instructions never has to re-reconcile the export.
	InstructionsRef string
	// Resources are the export's spec.resources. Like instructions, the
	// schemas they name are mirrored separately and joined at read time.
	Resources []ResourceRef
}

// ResourceRef is one resource an export serves and the APIResourceSchema,
// in the export's own cluster, that defines it.
type ResourceRef struct {
	Group    string
	Resource string
	Schema   string
}

// Schema is the directory's view of an APIResourceSchema: what the catalog
// shows of it.
type Schema struct {
	Kind     string
	Scope    string
	Versions []marketplacev1alpha1.OfferingResourceVersion
}

// Directory is safe for concurrent use.
type Directory struct {
	mu        sync.RWMutex
	ready     bool
	producers map[Key]Producer
	offerings map[Key]Offering
	// instructions holds the content of ConfigMaps offerings point at,
	// keyed like everything else by the cluster they live in.
	instructions map[Key]map[string]string
	// schemas holds the APIResourceSchemas offerings reference.
	schemas map[Key]Schema
}

// New returns an empty directory.
func New() *Directory {
	return &Directory{
		producers:    map[Key]Producer{},
		offerings:    map[Key]Offering{},
		instructions: map[Key]map[string]string{},
		schemas:      map[Key]Schema{},
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
func (d *Directory) Counts() (producers, offerings int) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return len(d.producers), len(d.offerings)
}

func (d *Directory) UpsertProducer(key Key, p Producer) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.producers[key] = p
}

func (d *Directory) RemoveProducer(key Key) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.producers, key)
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

// UpsertInstructions records the content of one instructions ConfigMap.
// Which offerings use it is decided at read time by name, so the two
// mirrors never have to know about each other.
func (d *Directory) UpsertInstructions(key Key, sections map[string]string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	copied := make(map[string]string, len(sections))
	for k, v := range sections {
		copied[k] = v
	}
	d.instructions[key] = copied
}

func (d *Directory) RemoveInstructions(key Key) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.instructions, key)
}

// UpsertSchema records one APIResourceSchema. As with instructions, which
// offerings use it is decided at read time by name.
func (d *Directory) UpsertSchema(key Key, s Schema) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.schemas[key] = s
}

func (d *Directory) RemoveSchema(key Key) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.schemas, key)
}

// ForgetCluster drops everything mirrored from one logical cluster — a
// workspace that unbound its export leaves nothing behind.
func (d *Directory) ForgetCluster(cluster string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for key := range d.producers {
		if key.Cluster == cluster {
			delete(d.producers, key)
		}
	}
	for key := range d.offerings {
		if key.Cluster == cluster {
			delete(d.offerings, key)
		}
	}
	for key := range d.instructions {
		if key.Cluster == cluster {
			delete(d.instructions, key)
		}
	}
	for key := range d.schemas {
		if key.Cluster == cluster {
			delete(d.schemas, key)
		}
	}
}

// Entry is one catalog listing: a published export joined with the
// Producer record that owns its workspace.
type Entry struct {
	Producer string
	Offering string
	// WorkspacePath is the canonical path of the producer's workspace —
	// what the APIBinding a consumer authors references as
	// spec.reference.export.path.
	WorkspacePath string
	Detail        Offering
	// Instructions is the referenced ConfigMap's content, or nil if the
	// offering names none or it has not been seen.
	Instructions map[string]string
	// Resources are the offering's resources joined with their schemas;
	// one whose schema has not been seen carries no versions.
	Resources []marketplacev1alpha1.OfferingResource
}

// Name is the entry's stable listing name, "<producer>.<export>".
func (e Entry) Name() string {
	return e.Producer + "." + e.Offering
}

// Catalog lists every offering whose workspace a Producer record names,
// sorted by name for stable output. An offering published from a workspace
// no record names is invisible — publishing into the void is not
// publishing, and it is also the case an unregistered workspace would use
// to put itself on the catalog by annotating its own export.
func (d *Directory) Catalog() []Entry {
	d.mu.RLock()
	defer d.mu.RUnlock()

	// The join is workspace → producer: the record says which logical
	// cluster it owns, and the offering is in one.
	producerByCluster := map[string]struct {
		name   string
		record Producer
	}{}
	for key, p := range d.producers {
		if p.WorkspaceCluster == "" {
			continue
		}
		producerByCluster[p.WorkspaceCluster] = struct {
			name   string
			record Producer
		}{key.Name, p}
	}

	var out []Entry
	for key, offering := range d.offerings {
		owner, known := producerByCluster[key.Cluster]
		if !known {
			continue
		}
		entry := Entry{
			Producer:      owner.name,
			Offering:      key.Name,
			WorkspacePath: owner.record.WorkspacePath,
			Detail:        offering,
		}
		// Instructions live next to the export, in the same cluster.
		if offering.InstructionsRef != "" {
			entry.Instructions = d.instructions[Key{Cluster: key.Cluster, Name: offering.InstructionsRef}]
		}
		// So do the schemas.
		for _, ref := range offering.Resources {
			r := marketplacev1alpha1.OfferingResource{Group: ref.Group, Resource: ref.Resource, Schema: ref.Schema}
			if s, seen := d.schemas[Key{Cluster: key.Cluster, Name: ref.Schema}]; seen {
				r.Kind, r.Scope, r.Versions = s.Kind, s.Scope, s.Versions
			}
			entry.Resources = append(entry.Resources, r)
		}
		out = append(out, entry)
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
