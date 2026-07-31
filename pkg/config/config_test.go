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

package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func validResource() ResourceConfiguration {
	return ResourceConfiguration{
		Export:   ExportReference{Path: "root:providers:s3", Name: "s3.example.com"},
		Group:    "s3.example.com",
		Resource: "bucketinfos",
		Webhook:  WebhookConfiguration{URL: "https://s3-info.example.com/ephemeral/bucketinfos"},
	}
}

func TestValidate(t *testing.T) {
	for name, tc := range map[string]struct {
		mutate      func(*Configuration)
		expectedErr string
	}{
		"a valid configuration": {
			mutate: func(*Configuration) {},
		},
		"no resources at all": {
			mutate:      func(c *Configuration) { c.Resources = nil },
			expectedErr: "resources",
		},
		"plain http is refused": {
			mutate:      func(c *Configuration) { c.Resources[0].Webhook.URL = "http://s3-info.example.com/x" },
			expectedErr: "must use the https scheme",
		},
		"a URL with credentials is refused": {
			mutate:      func(c *Configuration) { c.Resources[0].Webhook.URL = "https://user:pw@s3-info.example.com/x" },
			expectedErr: "must not have user info",
		},
		"a client certificate without its key is refused": {
			mutate:      func(c *Configuration) { c.Resources[0].Webhook.ClientCertFile = "/tls.crt" },
			expectedErr: "must be set together",
		},
		"a timeout beyond the cap is refused": {
			mutate:      func(c *Configuration) { c.Resources[0].Webhook.TimeoutSeconds = 120 },
			expectedErr: "must not exceed 30",
		},
		"the same resource twice": {
			mutate:      func(c *Configuration) { c.Resources = append(c.Resources, validResource()) },
			expectedErr: "Duplicate value",
		},
		"a resource without a group": {
			mutate:      func(c *Configuration) { c.Resources[0].Group = "" },
			expectedErr: "group",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			cfg := &Configuration{Resources: []ResourceConfiguration{validResource()}}
			tc.mutate(cfg)

			errs := cfg.Validate()
			if tc.expectedErr == "" {
				require.Empty(t, errs, "expected no errors, got %v", errs)
				return
			}

			require.NotEmpty(t, errs)
			require.Contains(t, errs.ToAggregate().Error(), tc.expectedErr)
		})
	}
}

func TestWebhookTimeout(t *testing.T) {
	require.Equal(t, 10*time.Second, WebhookConfiguration{}.Timeout(), "unset means the default")
	require.Equal(t, 5*time.Second, WebhookConfiguration{TimeoutSeconds: 5}.Timeout())
	require.Equal(t, 30*time.Second, WebhookConfiguration{TimeoutSeconds: 600}.Timeout(),
		"a slow webhook holds a request goroutine, so the cap is enforced at use, not only at validation")
}
