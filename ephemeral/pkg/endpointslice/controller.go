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

// Package endpointslice publishes a virtual workspace's address into the
// endpoint slices that APIExports reference.
//
// The kcp side of a virtual resource is a reference from an APIExport to an
// object with a URL in its status. Nothing fills that URL in for a virtual
// workspace kcp does not run, which is what this is for.
//
// It runs against a single workspace -- the one the APIExport lives in -- and
// that is not an accident of packaging. The slice kind is a CRD a provider
// applied to their own workspace, so it is only served there: there is no
// cluster-wide list of them to make, and no reason for this to hold credentials
// beyond the workspace it publishes into.
//
// It works on unstructured objects because the kind belongs to the provider,
// not to this repository, so there are no generated clients for it.
package endpointslice

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/logicalcluster/v3"

	ephemeralv1alpha1 "github.com/kcp-dev/contrib-virtual-ephemeral-resources-virtual-workspace/pkg/apis/ephemeral/v1alpha1"
)

// GroupVersionResource of the slices this controller owns.
var GroupVersionResource = schema.GroupVersionResource{
	Group:    "ephemeral.contrib.kcp.io",
	Version:  "v1alpha1",
	Resource: ephemeralv1alpha1.EndpointSliceResource,
}

// Options configure the controller.
type Options struct {
	// ExternalURL is where kcp should reach the virtual workspace, without a
	// path: https://ephemeral.example.com:6454.
	ExternalURL string

	// RootPathPrefix and VirtualWorkspaceName are the path the virtual
	// workspace answers on, and have to match what it was started with, or the
	// published URL points at nothing.
	RootPathPrefix       string
	VirtualWorkspaceName string

	// ExportName, when set, limits publishing to slices for that APIExport.
	// Empty means every slice in the workspace.
	ExportName string

	// Shards selects which shards should use the published URL. The zero value
	// publishes matchAll, meaning every shard -- one virtual workspace for the
	// whole installation, which is what a webhook-backed resource wants: there
	// is nothing shard-local about asking a provider a question.
	//
	// Naming shards here is how an installation running one virtual workspace
	// per shard would describe itself, but see Publish: this writes a single
	// endpoint, so several instances would overwrite each other rather than
	// each contributing one.
	Shards ephemeralv1alpha1.EndpointSelector

	// ResyncInterval bounds how long a newly created slice waits for its URL.
	//
	// A watch would be tidier. This is a loop because the kind is defined by a
	// provider's CRD and may not exist when this starts, which a typed informer
	// does not survive but a list does.
	ResyncInterval time.Duration
}

const defaultResyncInterval = 10 * time.Second

// Controller publishes a virtual workspace's address into the slices of one
// workspace.
type Controller struct {
	client dynamic.Interface
	opts   Options
}

// NewController returns a controller publishing into the workspace its client
// is scoped to.
func NewController(client dynamic.Interface, opts Options) (*Controller, error) {
	if opts.ExternalURL == "" {
		return nil, fmt.Errorf("an external URL is required: kcp has to be told where to reach the virtual workspace")
	}
	parsed, err := url.Parse(opts.ExternalURL)
	if err != nil {
		return nil, fmt.Errorf("invalid external URL %q: %w", opts.ExternalURL, err)
	}
	if parsed.Scheme != "https" {
		return nil, fmt.Errorf("external URL %q must use https", opts.ExternalURL)
	}
	if parsed.Host == "" {
		return nil, fmt.Errorf("external URL %q must have a host", opts.ExternalURL)
	}
	if opts.VirtualWorkspaceName == "" {
		return nil, fmt.Errorf("a virtual workspace name is required: it is part of the URL being published")
	}
	if opts.ResyncInterval == 0 {
		opts.ResyncInterval = defaultResyncInterval
	}

	return &Controller{client: client, opts: opts}, nil
}

// Start publishes until the context is cancelled.
func (c *Controller) Start(ctx context.Context) {
	logger := klog.FromContext(ctx)
	logger.Info("Publishing the virtual workspace address into endpoint slices",
		"externalURL", c.opts.ExternalURL, "every", c.opts.ResyncInterval)

	wait.UntilWithContext(ctx, func(ctx context.Context) {
		if err := c.Sync(ctx); err != nil {
			logger.Error(err, "failed to publish endpoints")
		}
	}, c.opts.ResyncInterval)
}

// Sync brings every slice in the workspace in line with where the virtual
// workspace is.
func (c *Controller) Sync(ctx context.Context) error {
	logger := klog.FromContext(ctx)

	list, err := c.client.Resource(GroupVersionResource).List(ctx, metav1.ListOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) || isNoMatch(err) {
			// The provider has not applied the CRD yet. Normal while an
			// installation is being set up, not worth a line per tick.
			logger.V(4).Info("no EphemeralResourceEndpointSlice kind served in this workspace")
			return nil
		}
		return fmt.Errorf("listing endpoint slices: %w", err)
	}

	var errs []string
	for i := range list.Items {
		slice := &list.Items[i]

		if c.opts.ExportName != "" {
			exportName, _, _ := unstructured.NestedString(slice.Object, "spec", "export", "name")
			if exportName != c.opts.ExportName {
				logger.V(4).Info("skipping a slice for another export",
					"slice", slice.GetName(), "export", exportName)
				continue
			}
		}

		if err := c.Publish(ctx, slice); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", slice.GetName(), err))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("failed to publish: %s", strings.Join(errs, "; "))
	}

	return nil
}

// Publish writes the virtual workspace's URL into one slice, if it is not
// already there.
//
// It writes a single endpoint and replaces whatever was in the list. That is
// correct for one virtual workspace serving every shard, and wrong for several
// instances each serving some: they would overwrite each other. Supporting that
// means each instance owning its own entry in the list.
func (c *Controller) Publish(ctx context.Context, slice *unstructured.Unstructured) error {
	logger := klog.FromContext(ctx).WithValues("slice", slice.GetName())

	exportName, found, err := unstructured.NestedString(slice.Object, "spec", "export", "name")
	if err != nil {
		return fmt.Errorf("reading spec.export.name: %w", err)
	}
	if !found || exportName == "" {
		return fmt.Errorf("spec.export.name is empty, so there is no URL to build")
	}

	// The URL addresses the logical cluster, not the workspace path, because
	// that is what the virtual workspace serves under. The object knows which
	// cluster it is in.
	cluster := logicalcluster.From(slice)
	if cluster.Empty() {
		return fmt.Errorf("the object carries no cluster annotation, so its URL cannot be built")
	}

	// Built from the typed shape so that the field names cannot drift from what
	// kcp reads: url, and a shards selector that is either matchAll or a label
	// selector, never both.
	shards := c.opts.Shards
	if !shards.MatchAll && shards.Selector == nil {
		// Saying nothing would ask kcp to match this URL by prefix against the
		// shard's own address, which is exactly what publishing avoids.
		shards.MatchAll = true
	}

	endpoint, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&ephemeralv1alpha1.EphemeralResourceEndpoint{
		URL:    c.URLFor(cluster, exportName),
		Shards: shards,
	})
	if err != nil {
		return fmt.Errorf("converting the endpoint: %w", err)
	}

	if equalEndpoints(slice, endpoint) {
		return nil
	}

	updated := slice.DeepCopy()
	if err := unstructured.SetNestedSlice(updated.Object, []interface{}{endpoint}, "status", "endpoints"); err != nil {
		return fmt.Errorf("setting status.endpoints: %w", err)
	}

	if _, err := c.client.Resource(GroupVersionResource).
		UpdateStatus(ctx, updated, metav1.UpdateOptions{}); err != nil {
		if apierrors.IsNotFound(err) {
			// Deleted underneath us; the next pass will not find it either.
			return nil
		}
		return fmt.Errorf("updating status: %w", err)
	}

	logger.Info("Published endpoint", "url", endpoint["url"], "shards", endpoint["shards"])

	return nil
}

// equalEndpoints reports whether the slice already advertises exactly this one
// endpoint, so that a steady state does not write on every tick.
func equalEndpoints(slice *unstructured.Unstructured, endpoint map[string]interface{}) bool {
	existing, found, err := unstructured.NestedSlice(slice.Object, "status", "endpoints")
	if err != nil || !found || len(existing) != 1 {
		return false
	}
	current, ok := existing[0].(map[string]interface{})
	if !ok {
		return false
	}
	return equalValues(current, endpoint)
}

func equalValues(a, b map[string]interface{}) bool {
	if len(a) != len(b) {
		return false
	}
	for key, want := range b {
		got, ok := a[key]
		if !ok {
			return false
		}
		switch want := want.(type) {
		case map[string]interface{}:
			gotMap, ok := got.(map[string]interface{})
			if !ok || !equalValues(gotMap, want) {
				return false
			}
		default:
			if got != want {
				return false
			}
		}
	}
	return true
}

// URLFor builds the address the virtual workspace serves an export's ephemeral
// resources on. It has to agree with what that server answers, which is
// <prefix>/<name>/<cluster>/<export>.
func (c *Controller) URLFor(cluster logicalcluster.Name, exportName string) string {
	return strings.TrimSuffix(c.opts.ExternalURL, "/") +
		"/" + strings.Trim(c.opts.RootPathPrefix, "/") +
		"/" + c.opts.VirtualWorkspaceName +
		"/" + cluster.String() +
		"/" + exportName
}

// isNoMatch reports whether the error says the kind is not served. The dynamic
// client surfaces this as a plain 404 in some paths and as a discovery failure
// in others, and neither is worth a log line every tick.
func isNoMatch(err error) bool {
	return err != nil && strings.Contains(err.Error(), "could not find the requested resource")
}
