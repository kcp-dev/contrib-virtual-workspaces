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

// Package apidefinitions turns the operator's webhook registry plus the
// APIExport/APIResourceSchema objects in kcp into the served API surface.
package apidefinitions

import (
	"context"
	"fmt"
	"strings"
	"sync"

	structuralschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	apiservervalidation "k8s.io/apiextensions-apiserver/pkg/apiserver/validation"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/registry/rest"
	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	"github.com/kcp-dev/sdk/apis/core"
	apisv1alpha1listers "github.com/kcp-dev/sdk/client/listers/apis/v1alpha1"
	apisv1alpha2listers "github.com/kcp-dev/sdk/client/listers/apis/v1alpha2"
	"github.com/kcp-dev/virtual-workspace-framework/pkg/dynamic/apidefinition"
	"github.com/kcp-dev/virtual-workspace-framework/pkg/dynamic/apiserver"
	dynamiccontext "github.com/kcp-dev/virtual-workspace-framework/pkg/dynamic/context"

	"github.com/kcp-dev/contrib-virtual-ephemeral-resources-virtual-workspace/pkg/config"
	"github.com/kcp-dev/contrib-virtual-ephemeral-resources-virtual-workspace/pkg/registry"
	"github.com/kcp-dev/contrib-virtual-ephemeral-resources-virtual-workspace/pkg/virtualworkspace/apidomainkey"
	"github.com/kcp-dev/contrib-virtual-ephemeral-resources-virtual-workspace/pkg/webhook"
)

var _ apidefinition.APIDefinitionSetGetter = (*Getter)(nil)

// ResourceEntry is one configured resource with its webhook client already
// built, so that a bad certificate path fails at startup rather than on the
// first request.
type ResourceEntry struct {
	// ExportRef is the export the entry was configured against, kept for error
	// messages.
	ExportRef config.ExportReference

	Group    string
	Resource string

	Client *webhook.Client
}

// Getter builds and caches the API definitions served for each APIExport.
//
// It deliberately is not a controller. The set of served resources is fixed by
// the configuration file, so the only thing that changes at runtime is the
// APIResourceSchema behind a resource. Rebuilding when the observed
// resourceVersion changes is cheaper and much smaller than a reconciler, and it
// cannot go stale relative to the informer.
type Getter struct {
	// entries is keyed by the export reference as written in the config, both
	// by path and (once resolved) by cluster name.
	entries map[string][]ResourceEntry

	getAPIExport func(cluster logicalcluster.Name, name string) (*apisv1alpha2.APIExport, error)
	getSchema    func(cluster logicalcluster.Name, name string) (*apisv1alpha1.APIResourceSchema, error)

	lock       sync.RWMutex
	mainConfig genericapiserver.CompletedConfig
	configured bool
	cached     map[dynamiccontext.APIDomainKey]*cacheEntry
}

type cacheEntry struct {
	// revision is the concatenation of the resourceVersions everything in the
	// set was built from. A change in any of them invalidates the whole set.
	revision string
	set      apidefinition.APIDefinitionSet
}

// NewGetter wires a Getter from the configuration and the informers' listers.
//
// The generic server config is not available this early; SetServerConfig must
// be called before the first request is served.
func NewGetter(
	cfg *config.Configuration,
	factory *webhook.Factory,
	localExports, globalExports apisv1alpha2listers.APIExportClusterLister,
	localSchemas, globalSchemas apisv1alpha1listers.APIResourceSchemaClusterLister,
) (*Getter, error) {
	entries := map[string][]ResourceEntry{}
	for i, r := range cfg.Resources {
		client, err := factory.New(r.Webhook)
		if err != nil {
			return nil, fmt.Errorf("resources[%d] (%s/%s): %w", i, r.Group, r.Resource, err)
		}

		key := strings.ToLower(r.Export.Path) + "|" + r.Export.Name
		entries[key] = append(entries[key], ResourceEntry{
			ExportRef: r.Export,
			Group:     r.Group,
			Resource:  r.Resource,
			Client:    client,
		})
	}

	return &Getter{
		entries: entries,
		cached:  map[dynamiccontext.APIDomainKey]*cacheEntry{},
		getAPIExport: func(cluster logicalcluster.Name, name string) (*apisv1alpha2.APIExport, error) {
			export, err := localExports.Cluster(cluster).Get(name)
			if err == nil {
				return export, nil
			}
			if !apierrors.IsNotFound(err) {
				return nil, err
			}
			return globalExports.Cluster(cluster).Get(name)
		},
		getSchema: func(cluster logicalcluster.Name, name string) (*apisv1alpha1.APIResourceSchema, error) {
			sch, err := localSchemas.Cluster(cluster).Get(name)
			if err == nil {
				return sch, nil
			}
			if !apierrors.IsNotFound(err) {
				return nil, err
			}
			return globalSchemas.Cluster(cluster).Get(name)
		},
	}, nil
}

// SetServerConfig hands the Getter the generic server config it needs to build
// serving infos. It is called once, from BootstrapAPISetManagement, before the
// server starts accepting requests.
func (g *Getter) SetServerConfig(mainConfig genericapiserver.CompletedConfig) {
	g.lock.Lock()
	defer g.lock.Unlock()
	g.mainConfig = mainConfig
	g.configured = true
}

// HasEntriesFor reports whether anything is configured for the given export.
// The authorizer uses it to reject requests for exports this virtual workspace
// does not serve before doing any further work.
func (g *Getter) HasEntriesFor(export *apisv1alpha2.APIExport) bool {
	return len(g.entriesFor(export)) > 0
}

// entriesFor matches the configured entries against an export, accepting either
// the export's workspace path or its logical cluster name in the config.
func (g *Getter) entriesFor(export *apisv1alpha2.APIExport) []ResourceEntry {
	cluster := logicalcluster.From(export)

	keys := []string{strings.ToLower(cluster.String()) + "|" + export.Name}
	if path := export.Annotations[core.LogicalClusterPathAnnotationKey]; path != "" {
		keys = append(keys, strings.ToLower(path)+"|"+export.Name)
	}

	for _, key := range keys {
		if entries, ok := g.entries[key]; ok {
			return entries
		}
	}

	return nil
}

// GetAPIDefinitionSet implements apidefinition.APIDefinitionSetGetter.
func (g *Getter) GetAPIDefinitionSet(ctx context.Context, key dynamiccontext.APIDomainKey) (apidefinition.APIDefinitionSet, bool, error) {
	parsed, err := apidomainkey.Parse(key)
	if err != nil {
		return nil, false, err
	}

	export, err := g.getAPIExport(parsed.Cluster, parsed.Name)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, false, nil
		}
		return nil, false, err
	}

	entries := g.entriesFor(export)
	if len(entries) == 0 {
		// Not an export this virtual workspace serves. Reporting "no such API
		// domain" makes the framework fall through to its delegate, which 404s.
		return nil, false, nil
	}

	specs, revision, err := g.resolve(export, entries)
	if err != nil {
		return nil, false, err
	}

	g.lock.RLock()
	cached, ok := g.cached[key]
	mainConfig, configured := g.mainConfig, g.configured
	g.lock.RUnlock()
	if ok && cached.revision == revision {
		return cached.set, true, nil
	}
	if !configured {
		return nil, false, fmt.Errorf("the ephemeral virtual workspace is not initialized yet")
	}

	set, err := g.build(mainConfig, specs)
	if err != nil {
		return nil, false, err
	}

	g.lock.Lock()
	defer g.lock.Unlock()
	// Another request may have built the same set concurrently; tear ours down
	// rather than leaking it.
	if existing, ok := g.cached[key]; ok && existing.revision == revision {
		for _, def := range set {
			def.TearDown()
		}
		return existing.set, true, nil
	}
	if existing, ok := g.cached[key]; ok {
		for _, def := range existing.set {
			def.TearDown()
		}
	}
	g.cached[key] = &cacheEntry{revision: revision, set: set}

	return set, true, nil
}

// servedResource is everything needed to build one APIDefinition.
type servedResource struct {
	schema  *apisv1alpha1.APIResourceSchema
	version string
	entry   ResourceEntry
}

// resolve maps configured entries to the schemas the export currently points
// at, and returns a revision string that changes whenever any input does.
func (g *Getter) resolve(export *apisv1alpha2.APIExport, entries []ResourceEntry) ([]servedResource, string, error) {
	cluster := logicalcluster.From(export)

	var (
		out       []servedResource
		revisions = []string{export.ResourceVersion}
	)

	for _, entry := range entries {
		var schemaName string
		for _, r := range export.Spec.Resources {
			if r.Group == entry.Group && r.Name == entry.Resource {
				schemaName = r.Schema
				break
			}
		}
		if schemaName == "" {
			// Configured but not exported. This is an operator error, and it is
			// better surfaced as a missing resource than as a server error on
			// the whole export.
			klog.Background().Error(nil, "configured ephemeral resource is not exported by the APIExport",
				"export", entry.ExportRef.String(), "group", entry.Group, "resource", entry.Resource)
			continue
		}

		sch, err := g.getSchema(cluster, schemaName)
		if err != nil {
			if apierrors.IsNotFound(err) {
				klog.Background().Error(err, "APIResourceSchema referenced by the APIExport not found",
					"export", entry.ExportRef.String(), "schema", schemaName)
				continue
			}
			return nil, "", err
		}
		revisions = append(revisions, schemaName+"="+sch.ResourceVersion)

		for i := range sch.Spec.Versions {
			v := sch.Spec.Versions[i]
			if !v.Served {
				continue
			}
			out = append(out, servedResource{schema: sch, version: v.Name, entry: entry})
		}
	}

	return out, strings.Join(revisions, ","), nil
}

func (g *Getter) build(mainConfig genericapiserver.CompletedConfig, specs []servedResource) (apidefinition.APIDefinitionSet, error) {
	set := apidefinition.APIDefinitionSet{}

	for _, spec := range specs {
		gvr := schema.GroupVersionResource{
			Group:    spec.schema.Spec.Group,
			Version:  spec.version,
			Resource: spec.schema.Spec.Names.Plural,
		}

		def, err := apiserver.CreateServingInfoFor(
			mainConfig,
			spec.schema,
			spec.version,
			provideEphemeralRestStorage(spec.entry.Client),
		)
		if err != nil {
			for _, built := range set {
				built.TearDown()
			}
			return nil, fmt.Errorf("building serving info for %s: %w", gvr, err)
		}

		set[gvr] = def
	}

	return set, nil
}

// provideEphemeralRestStorage returns the framework's RestProviderFunc that
// installs the webhook-backed storage. No subresource storages are returned:
// only create is served, and there is no named object to hang /status off.
func provideEphemeralRestStorage(client *webhook.Client) apiserver.RestProviderFunc {
	return func(
		resource schema.GroupVersionResource,
		kind schema.GroupVersionKind,
		listKind schema.GroupVersionKind,
		typer runtime.ObjectTyper,
		tableConvertor rest.TableConvertor,
		namespaceScoped bool,
		schemaValidator apiservervalidation.SchemaValidator,
		subresourcesSchemaValidator map[string]apiservervalidation.SchemaValidator,
		structuralSchema *structuralschema.Structural,
	) (rest.Storage, map[string]rest.Storage) {
		singular := strings.ToLower(kind.Kind)

		return registry.New(
			resource,
			kind,
			singular,
			namespaceScoped,
			tableConvertor,
			schemaValidator,
			client,
		), nil
	}
}
