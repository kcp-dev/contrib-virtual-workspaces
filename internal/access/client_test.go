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

package access

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	accessv1alpha1 "github.com/kcp-dev/contrib-access-virtual-workspace/sdk/apis/access/v1alpha1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/client-go/rest"
)

// fakeAccessVW answers SelfClusterAccessReview POSTs and records how often and
// as whom it was asked.
func fakeAccessVW(t *testing.T, hits *atomic.Int64, lastImpersonated *atomic.Value) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wantPath := "/apis/" + accessv1alpha1.SchemeGroupVersion.Group + "/" +
			accessv1alpha1.SchemeGroupVersion.Version + "/selfclusteraccessreviews"
		if r.Method != http.MethodPost || r.URL.Path != wantPath {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		hits.Add(1)
		lastImpersonated.Store(r.Header.Get("Impersonate-User"))

		resp := accessv1alpha1.SelfClusterAccessReview{
			TypeMeta: metav1.TypeMeta{
				APIVersion: accessv1alpha1.SchemeGroupVersion.String(),
				Kind:       "SelfClusterAccessReview",
			},
			Status: accessv1alpha1.SelfClusterAccessReviewStatus{
				Clusters: []accessv1alpha1.AccessEndpointSlice{
					{ClusterName: "root:alpha", Endpoint: "https://kcp.example/clusters/root:alpha"},
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			t.Errorf("encoding response: %v", err)
		}
	}))
}

func newTestClient(t *testing.T, url string, ttl time.Duration) *Client {
	t.Helper()
	c, err := New(Options{
		BaseURL:      url,
		Impersonator: &rest.Config{Host: url},
		CacheTTL:     ttl,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

// TestWorkspaces exercises the full request path — building the REST client
// must not fail (a config without a NegotiatedSerializer is rejected by
// rest.RESTClientFor) and the response must round-trip.
func TestWorkspaces(t *testing.T) {
	var hits atomic.Int64
	var impersonated atomic.Value
	srv := fakeAccessVW(t, &hits, &impersonated)
	defer srv.Close()

	c := newTestClient(t, srv.URL, time.Minute)

	alice := &user.DefaultInfo{Name: "alice", Groups: []string{"team-a"}}
	workspaces, err := c.Workspaces(context.Background(), alice)
	if err != nil {
		t.Fatalf("Workspaces: %v", err)
	}
	if len(workspaces) != 1 || workspaces[0].Name != "root:alpha" ||
		workspaces[0].Endpoint != "https://kcp.example/clusters/root:alpha" {
		t.Fatalf("unexpected workspaces: %+v", workspaces)
	}
	if got := impersonated.Load(); got != "alice" {
		t.Fatalf("impersonated %q, want alice", got)
	}
}

func TestWorkspacesCache(t *testing.T) {
	var hits atomic.Int64
	var impersonated atomic.Value
	srv := fakeAccessVW(t, &hits, &impersonated)
	defer srv.Close()

	c := newTestClient(t, srv.URL, time.Minute)
	ctx := context.Background()

	alice := &user.DefaultInfo{Name: "alice", Groups: []string{"team-a"}}
	for range 3 {
		if _, err := c.Workspaces(ctx, alice); err != nil {
			t.Fatalf("Workspaces: %v", err)
		}
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("expected 1 upstream call for a cached identity, got %d", got)
	}

	// A group change is a different identity and must miss the cache.
	promoted := &user.DefaultInfo{Name: "alice", Groups: []string{"team-a", "admins"}}
	if _, err := c.Workspaces(ctx, promoted); err != nil {
		t.Fatalf("Workspaces: %v", err)
	}
	if got := hits.Load(); got != 2 {
		t.Fatalf("expected group change to miss the cache, got %d upstream calls", got)
	}

	// So must an extra change.
	scoped := &user.DefaultInfo{Name: "alice", Groups: []string{"team-a"},
		Extra: map[string][]string{"scopes": {"cluster:root"}}}
	if _, err := c.Workspaces(ctx, scoped); err != nil {
		t.Fatalf("Workspaces: %v", err)
	}
	if got := hits.Load(); got != 3 {
		t.Fatalf("expected extra change to miss the cache, got %d upstream calls", got)
	}

	// Invalidate drops the entry.
	c.Invalidate(alice)
	if _, err := c.Workspaces(ctx, alice); err != nil {
		t.Fatalf("Workspaces: %v", err)
	}
	if got := hits.Load(); got != 4 {
		t.Fatalf("expected invalidated identity to miss the cache, got %d upstream calls", got)
	}
}

func TestCachePrunesExpiredEntries(t *testing.T) {
	var hits atomic.Int64
	var impersonated atomic.Value
	srv := fakeAccessVW(t, &hits, &impersonated)
	defer srv.Close()

	c := newTestClient(t, srv.URL, time.Millisecond)
	ctx := context.Background()

	if _, err := c.Workspaces(ctx, &user.DefaultInfo{Name: "alice"}); err != nil {
		t.Fatalf("Workspaces: %v", err)
	}
	time.Sleep(5 * time.Millisecond)
	if _, err := c.Workspaces(ctx, &user.DefaultInfo{Name: "bob"}); err != nil {
		t.Fatalf("Workspaces: %v", err)
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.cache) != 1 {
		t.Fatalf("expected expired entries to be pruned on write, cache holds %d entries", len(c.cache))
	}
	if _, ok := c.cache[cacheKey(&user.DefaultInfo{Name: "bob"})]; !ok {
		t.Fatal("expected the fresh entry to survive pruning")
	}
}
