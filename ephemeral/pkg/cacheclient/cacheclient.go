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

// Package cacheclient builds a rest.Config that talks to a kcp cache server.
//
// kcp keeps the equivalent helpers in its own module, but the kcp module cannot
// be consumed as a dependency: its published go.mod pins
// github.com/kcp-dev/sdk at v0.0.0 and resolves it with a replace directive
// pointing into ./staging, and replace directives in a dependency are ignored
// by the Go tool. The two round trippers this virtual workspace needs are small
// enough to carry here.
package cacheclient

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"k8s.io/client-go/rest"
)

const (
	// servicePrefix is where a kcp shard serves the cache API.
	servicePrefix = "/services/cache"

	// wildcardShardPrefix asks the cache server for content from every shard.
	// Everything this virtual workspace reads (APIExports, APIResourceSchemas)
	// may be owned by any shard, and it only ever reads.
	wildcardShardPrefix = "/shards/*"
)

// RestConfig returns a copy of base pointed at the cache API, reading across
// all shards.
func RestConfig(base *rest.Config) *rest.Config {
	cfg := rest.CopyConfig(base)
	cfg.Wrap(func(rt http.RoundTripper) http.RoundTripper {
		return &prefixRoundTripper{delegate: rt, prefix: servicePrefix + wildcardShardPrefix}
	})
	return cfg
}

// prefixRoundTripper prepends a fixed path prefix to every request.
type prefixRoundTripper struct {
	delegate http.RoundTripper
	prefix   string
}

func (c *prefixRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if strings.HasPrefix(req.URL.Path, c.prefix) {
		return c.delegate.RoundTrip(req)
	}

	req = req.Clone(req.Context())

	prefix := c.prefix
	if len(req.URL.Path) > 0 && req.URL.Path[0] != '/' {
		prefix += "/"
	}
	req.URL.Path = fmt.Sprintf("%s%s", prefix, req.URL.Path)

	// Regenerate the URL so that RawPath is updated alongside Path.
	newURL, err := url.Parse(req.URL.String())
	if err != nil {
		return nil, fmt.Errorf("rewriting request path for the cache server: %w", err)
	}
	req.URL = newURL

	return c.delegate.RoundTrip(req)
}

func (c *prefixRoundTripper) WrappedRoundTripper() http.RoundTripper {
	return c.delegate
}
