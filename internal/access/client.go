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

// Package access asks the access virtual workspace which workspaces a caller
// can see.
package access

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	accessv1alpha1 "github.com/kcp-dev/contrib-access-virtual-workspace/pkg/apis/access/v1alpha1"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
)

var codecs = func() serializer.CodecFactory {
	scheme := runtime.NewScheme()
	utilruntime.Must(accessv1alpha1.AddToScheme(scheme))
	return serializer.NewCodecFactory(scheme)
}()

// Workspace is one workspace a caller may use.
type Workspace struct {
	Name     string
	Endpoint string
}

// Options configures a Client.
type Options struct {
	// BaseURL is the access virtual workspace's address, up to and including
	// its /services/access prefix.
	BaseURL string

	// CAFile verifies the access VW's serving certificate. Empty uses the
	// system pool.
	CAFile string

	// Impersonator is this server's own credential. SelfClusterAccessReview
	// answers for whoever the request authenticates as, so asking on a caller's
	// behalf means impersonating them.
	Impersonator *rest.Config

	// CacheTTL bounds how long an answer is reused per caller.
	CacheTTL time.Duration
}

// Client resolves a caller's workspaces from the access virtual workspace.
//
// Results are cached per identity for CacheTTL, because a single MCP session
// makes many tool calls and each one needs the same list. The cache is the
// reason an HTTP hop is acceptable here at all.
type Client struct {
	opts Options

	mu    sync.Mutex
	cache map[string]cacheEntry
}

type cacheEntry struct {
	workspaces []Workspace
	expires    time.Time
}

// New returns a Client. It does not contact the access virtual workspace;
// failures surface on the first Workspaces call.
func New(opts Options) (*Client, error) {
	if opts.BaseURL == "" {
		return nil, fmt.Errorf("access virtual workspace URL is required")
	}
	if opts.Impersonator == nil {
		return nil, fmt.Errorf("impersonation config is required")
	}
	if opts.CacheTTL <= 0 {
		return nil, fmt.Errorf("cache TTL must be positive")
	}

	return &Client{
		opts:  opts,
		cache: map[string]cacheEntry{},
	}, nil
}

// Workspaces returns the workspaces u may use, from cache when fresh.
func (c *Client) Workspaces(ctx context.Context, u user.Info) ([]Workspace, error) {
	key := cacheKey(u)

	c.mu.Lock()
	if entry, ok := c.cache[key]; ok && time.Now().Before(entry.expires) {
		c.mu.Unlock()
		return entry.workspaces, nil
	}
	c.mu.Unlock()

	workspaces, err := c.review(ctx, u)
	if err != nil {
		return nil, err
	}

	now := time.Now()
	c.mu.Lock()

	for k, entry := range c.cache {
		if now.After(entry.expires) {
			delete(c.cache, k)
		}
	}
	c.cache[key] = cacheEntry{workspaces: workspaces, expires: now.Add(c.opts.CacheTTL)}
	c.mu.Unlock()

	klog.FromContext(ctx).V(4).Info("resolved caller workspaces",
		"username", u.GetName(), "groups", u.GetGroups(), "workspaces", len(workspaces))

	return workspaces, nil
}

func (c *Client) review(ctx context.Context, u user.Info) ([]Workspace, error) {
	cfg := rest.CopyConfig(c.opts.Impersonator)
	cfg.Host = c.opts.BaseURL
	cfg.APIPath = "/apis"
	cfg.GroupVersion = &accessv1alpha1.SchemeGroupVersion
	cfg.NegotiatedSerializer = codecs.WithoutConversion()
	cfg.Impersonate = rest.ImpersonationConfig{
		UserName: u.GetName(),
		Groups:   u.GetGroups(),
		Extra:    u.GetExtra(),
	}
	if c.opts.CAFile != "" {
		cfg.CAFile = c.opts.CAFile
		cfg.CAData = nil
	}

	client, err := rest.RESTClientFor(cfg)
	if err != nil {
		return nil, fmt.Errorf("building access client: %w", err)
	}

	result := &accessv1alpha1.SelfClusterAccessReview{}
	if err := client.Post().
		Resource("selfclusteraccessreviews").
		Body(&accessv1alpha1.SelfClusterAccessReview{}).
		Do(ctx).
		Into(result); err != nil {
		return nil, fmt.Errorf("SelfClusterAccessReview for %q: %w", u.GetName(), err)
	}

	workspaces := make([]Workspace, 0, len(result.Status.Clusters))
	for _, cluster := range result.Status.Clusters {
		workspaces = append(workspaces, Workspace{
			Name:     cluster.ClusterName,
			Endpoint: cluster.Endpoint,
		})
	}

	return workspaces, nil
}

// Invalidate drops any cached answer for u.
func (c *Client) Invalidate(u user.Info) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.cache, cacheKey(u))
}

func cacheKey(u user.Info) string {
	key := u.GetName()
	for _, g := range u.GetGroups() {
		key += "\x00" + g
	}
	extra := u.GetExtra()
	extraKeys := make([]string, 0, len(extra))
	for k := range extra {
		extraKeys = append(extraKeys, k)
	}
	sort.Strings(extraKeys)
	for _, k := range extraKeys {
		key += "\x01" + k
		for _, v := range extra[k] {
			key += "\x02" + v
		}
	}
	return key
}
