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

package builder

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDigestURL(t *testing.T) {
	const prefix = "/services/apiexport/"

	for name, tc := range map[string]struct {
		path string

		accepted      bool
		cluster       string
		wildcard      bool
		domainKey     string
		prefixToStrip string
	}{
		"a consumer create, as the shard proxies it": {
			path:          "/services/apiexport/provider-ws/s3.example.com/clusters/consumer-ws/apis/s3.example.com/v1alpha1/bucketinfos",
			accepted:      true,
			cluster:       "consumer-ws",
			domainKey:     "provider-ws/s3.example.com",
			prefixToStrip: "/services/apiexport/provider-ws/s3.example.com/clusters/consumer-ws",
		},
		"a namespaced create": {
			path:          "/services/apiexport/provider-ws/s3.example.com/clusters/consumer-ws/apis/s3.example.com/v1alpha1/namespaces/team-a/bucketinfos",
			accepted:      true,
			cluster:       "consumer-ws",
			domainKey:     "provider-ws/s3.example.com",
			prefixToStrip: "/services/apiexport/provider-ws/s3.example.com/clusters/consumer-ws",
		},
		"discovery, which the shard issues to learn the verbs": {
			path:          "/services/apiexport/provider-ws/s3.example.com/clusters/consumer-ws/apis/s3.example.com/v1alpha1",
			accepted:      true,
			cluster:       "consumer-ws",
			domainKey:     "provider-ws/s3.example.com",
			prefixToStrip: "/services/apiexport/provider-ws/s3.example.com/clusters/consumer-ws",
		},
		"a wildcard request is parsed, and rejected later by the authorizer": {
			path:          "/services/apiexport/provider-ws/s3.example.com/clusters/*/apis/s3.example.com/v1alpha1/bucketinfos",
			accepted:      true,
			wildcard:      true,
			domainKey:     "provider-ws/s3.example.com",
			prefixToStrip: "/services/apiexport/provider-ws/s3.example.com/clusters/*",
		},
		"a different virtual workspace": {
			path:     "/services/replication/provider-ws/slice/clusters/consumer-ws/apis/s3.example.com/v1alpha1/bucketinfos",
			accepted: false,
		},
		"no /clusters/ segment": {
			path:     "/services/apiexport/provider-ws/s3.example.com/apis/s3.example.com/v1alpha1/bucketinfos",
			accepted: false,
		},
		"missing the export name": {
			path:     "/services/apiexport/provider-ws/clusters/consumer-ws/apis/s3.example.com/v1alpha1",
			accepted: false,
		},
		"an empty export cluster": {
			path:     "/services/apiexport//s3.example.com/clusters/consumer-ws/apis/s3.example.com/v1alpha1",
			accepted: false,
		},
		"too short to be anything": {
			path:     "/services/apiexport/provider-ws",
			accepted: false,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			cluster, domainKey, prefixToStrip, accepted := digestURL(tc.path, prefix)

			require.Equal(t, tc.accepted, accepted)
			if !tc.accepted {
				return
			}

			require.Equal(t, tc.wildcard, cluster.Wildcard)
			require.Equal(t, tc.cluster, cluster.Name.String())
			require.Equal(t, tc.domainKey, string(domainKey))
			require.Equal(t, tc.prefixToStrip, prefixToStrip)
		})
	}
}
