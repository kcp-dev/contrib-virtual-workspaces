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

package webhook

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	ephemeralv1alpha1 "github.com/kcp-dev/contrib-virtual-workspaces/ephemeral/pkg/apis/ephemeral/v1alpha1"
	"github.com/kcp-dev/contrib-virtual-workspaces/ephemeral/pkg/config"
)

func TestDenyPrivateAddresses(t *testing.T) {
	for name, tc := range map[string]struct {
		address string
		denied  bool
	}{
		"loopback":                {address: "127.0.0.1:443", denied: true},
		"IPv6 loopback":           {address: "[::1]:443", denied: true},
		"the cloud metadata IP":   {address: "169.254.169.254:80", denied: true},
		"an RFC1918 pod address":  {address: "10.244.1.7:8443", denied: true},
		"another RFC1918 address": {address: "192.168.1.10:443", denied: true},
		"a 172.16/12 address":     {address: "172.16.0.5:443", denied: true},
		"unspecified":             {address: "0.0.0.0:443", denied: true},
		"a public address":        {address: "93.184.216.34:443", denied: false},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := denyPrivateAddresses("tcp", tc.address, nil)
			if tc.denied {
				require.Error(t, err, "%s must be refused", tc.address)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestGuardRefusesLoopbackEndToEnd(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("the webhook must not be reached")
	}))
	defer server.Close()

	// Default options: the guard is on.
	client, err := NewFactory(Options{}).New(config.WebhookConfiguration{URL: server.URL})
	require.NoError(t, err)

	_, err = client.Call(context.Background(), &ephemeralv1alpha1.EphemeralReview{
		Request: &ephemeralv1alpha1.EphemeralRequest{UID: "uid"},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "loopback")
}

func TestCallRejectsOversizedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(strings.Repeat("x", 4096)))
	}))
	defer server.Close()

	client, err := NewFactory(Options{AllowPrivateAddresses: true, MaxResponseBytes: 1024}).
		New(config.WebhookConfiguration{URL: server.URL})
	require.NoError(t, err)

	_, err = client.Call(context.Background(), &ephemeralv1alpha1.EphemeralReview{
		Request: &ephemeralv1alpha1.EphemeralRequest{UID: "uid"},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "exceeds 1024 bytes")
}

func TestCallRejectsNon200(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer server.Close()

	client, err := NewFactory(Options{AllowPrivateAddresses: true}).New(config.WebhookConfiguration{URL: server.URL})
	require.NoError(t, err)

	_, err = client.Call(context.Background(), &ephemeralv1alpha1.EphemeralReview{
		Request: &ephemeralv1alpha1.EphemeralRequest{UID: "uid"},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "HTTP 502")
}

func TestCallDoesNotFollowRedirects(t *testing.T) {
	// A redirect would hand the review, which carries the user's identity, to
	// a host the operator never configured.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Redirect(w, &http.Request{}, "https://elsewhere.example.com/", http.StatusFound)
	}))
	defer server.Close()

	client, err := NewFactory(Options{AllowPrivateAddresses: true}).New(config.WebhookConfiguration{URL: server.URL})
	require.NoError(t, err)

	_, err = client.Call(context.Background(), &ephemeralv1alpha1.EphemeralReview{
		Request: &ephemeralv1alpha1.EphemeralRequest{UID: "uid"},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "redirects are not followed")
}
