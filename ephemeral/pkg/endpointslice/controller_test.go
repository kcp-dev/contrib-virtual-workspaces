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

package endpointslice

import (
	"reflect"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/kcp-dev/logicalcluster/v3"

	ephemeralv1alpha1 "github.com/kcp-dev/contrib-virtual-workspaces/ephemeral/pkg/apis/ephemeral/v1alpha1"
)

// The published URL is what a shard proxies to, and this server only answers on
// <prefix>/<name>/<cluster>/<export>. Getting it wrong produces a 404 at the
// first request and nothing before that, so it is worth pinning down.
func TestURLFor(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		external string
		prefix   string
		vwName   string
		want     string
	}{
		{
			name:     "the path this server answers on",
			external: "https://ephemeral.example.com:6454",
			prefix:   "/services",
			vwName:   "apiexport",
			want:     "https://ephemeral.example.com:6454/services/apiexport/root-org-ws/s3.example.com",
		},
		{
			name:     "a trailing slash on the external URL does not double up",
			external: "https://ephemeral.example.com:6454/",
			prefix:   "/services",
			vwName:   "apiexport",
			want:     "https://ephemeral.example.com:6454/services/apiexport/root-org-ws/s3.example.com",
		},
		{
			name:     "a prefix without slashes is handled the same",
			external: "https://ephemeral.example.com:6454",
			prefix:   "services",
			vwName:   "apiexport",
			want:     "https://ephemeral.example.com:6454/services/apiexport/root-org-ws/s3.example.com",
		},
		{
			name:     "a virtual workspace served under another name",
			external: "https://ephemeral.example.com:6454",
			prefix:   "/services",
			vwName:   "ephemeral",
			want:     "https://ephemeral.example.com:6454/services/ephemeral/root-org-ws/s3.example.com",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			c := &Controller{opts: Options{
				ExternalURL:          tc.external,
				RootPathPrefix:       tc.prefix,
				VirtualWorkspaceName: tc.vwName,
			}}

			got := c.URLFor(logicalcluster.Name("root-org-ws"), "s3.example.com")
			if got != tc.want {
				t.Fatalf("URLFor() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestNewControllerRejectsUnusableURLs(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name        string
		externalURL string
	}{
		{"empty", ""},
		{"plain http", "http://ephemeral.example.com:6454"},
		{"no host", "https://"},
		{"not a URL", "://nonsense"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			opts := Options{ExternalURL: tc.externalURL, VirtualWorkspaceName: "ephemeral"}
			if _, err := NewController(nil, opts); err == nil {
				t.Fatalf("NewController() accepted %q; a URL kcp cannot reach is worse than a startup failure", tc.externalURL)
			}
		})
	}
}

// The virtual workspace name is part of the URL, so publishing without one
// would advertise a path that answers nothing.
func TestNewControllerRequiresAVirtualWorkspaceName(t *testing.T) {
	t.Parallel()

	_, err := NewController(nil, Options{ExternalURL: "https://ephemeral.example.com:6454"})
	if err == nil {
		t.Fatal("NewController() accepted an empty virtual workspace name")
	}
}

// What lands in status has to be the shape kcp reads. matchAll and a selector
// are different things there -- matchAll is the whole installation, a selector
// is a subset -- and saying neither means "match this URL by prefix against the
// shard's own address", which is what publishing exists to avoid.
func TestPublishedSelector(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		opts ephemeralv1alpha1.EndpointSelector
		want map[string]interface{}
	}{
		{
			name: "nothing said publishes matchAll, not an empty selector",
			want: map[string]interface{}{"matchAll": true},
		},
		{
			name: "matchAll stays matchAll",
			opts: ephemeralv1alpha1.EndpointSelector{MatchAll: true},
			want: map[string]interface{}{"matchAll": true},
		},
		{
			name: "labels are published under selector",
			opts: ephemeralv1alpha1.EndpointSelector{
				Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"region": "eu"}},
			},
			want: map[string]interface{}{
				"selector": map[string]interface{}{
					"matchLabels": map[string]interface{}{"region": "eu"},
				},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			c := &Controller{opts: Options{
				ExternalURL:          "https://ephemeral.example.com:6454",
				RootPathPrefix:       "/services",
				VirtualWorkspaceName: "ephemeral",
				Shards:               tc.opts,
			}}

			shards := c.opts.Shards
			if !shards.MatchAll && shards.Selector == nil {
				shards.MatchAll = true
			}
			endpoint, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&ephemeralv1alpha1.EphemeralResourceEndpoint{
				URL:    c.URLFor("cluster", "export"),
				Shards: shards,
			})
			if err != nil {
				t.Fatal(err)
			}

			got, _ := endpoint["shards"].(map[string]interface{})
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("published shards = %v, want %v", got, tc.want)
			}
		})
	}
}
