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

// Package webhook implements the client half of the EphemeralReview contract.
package webhook

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"syscall"
	"time"

	"k8s.io/client-go/transport"

	ephemeralv1alpha1 "github.com/kcp-dev/contrib-virtual-workspaces/ephemeral/pkg/apis/ephemeral/v1alpha1"
	"github.com/kcp-dev/contrib-virtual-workspaces/ephemeral/pkg/config"
)

// DefaultMaxResponseBytes bounds how large a webhook response body may be. A
// webhook must not be able to return an object larger than kcp would accept on
// the way in.
const DefaultMaxResponseBytes int64 = 3 * 1024 * 1024

// Options tune every client built by a Factory.
type Options struct {
	// AllowPrivateAddresses permits webhook endpoints that resolve to loopback,
	// link-local, or private addresses.
	//
	// Off by default: a virtual workspace typically runs inside a cluster, and
	// an endpoint pointing at 169.254.169.254 or a pod IP would turn it into a
	// confused deputy that reads cluster-internal data on behalf of whoever
	// configured it.
	AllowPrivateAddresses bool

	// MaxResponseBytes bounds the response body. Defaults to
	// DefaultMaxResponseBytes.
	MaxResponseBytes int64
}

// Factory builds and caches one Client per configured webhook.
type Factory struct {
	opts Options
}

// NewFactory returns a Factory applying opts to every client it builds.
func NewFactory(opts Options) *Factory {
	if opts.MaxResponseBytes <= 0 {
		opts.MaxResponseBytes = DefaultMaxResponseBytes
	}
	return &Factory{opts: opts}
}

// New builds a Client for a webhook configuration. Certificates are read once,
// here, so that a misconfigured entry fails at startup rather than on the first
// request.
func (f *Factory) New(cfg config.WebhookConfiguration) (*Client, error) {
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}

	if cfg.CABundleFile != "" {
		pem, err := os.ReadFile(cfg.CABundleFile)
		if err != nil {
			return nil, fmt.Errorf("reading caBundleFile: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("caBundleFile %s contains no PEM certificates", cfg.CABundleFile)
		}
		tlsConfig.RootCAs = pool
	}

	if cfg.ClientCertFile != "" {
		cert, err := tls.LoadX509KeyPair(cfg.ClientCertFile, cfg.ClientKeyFile)
		if err != nil {
			return nil, fmt.Errorf("loading client certificate: %w", err)
		}
		tlsConfig.Certificates = []tls.Certificate{cert}
	}

	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	if !f.opts.AllowPrivateAddresses {
		dialer.Control = denyPrivateAddresses
	}

	rt := http.RoundTripper(&http.Transport{
		TLSClientConfig:       tlsConfig,
		DialContext:           dialer.DialContext,
		MaxIdleConnsPerHost:   10,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		// The review carries the user's identity in its body; never follow a
		// redirect that would hand it to a different host.
		Proxy: http.ProxyFromEnvironment,
	})
	rt = transport.DebugWrappers(rt)

	return &Client{
		url:              cfg.URL,
		maxResponseBytes: f.opts.MaxResponseBytes,
		client: &http.Client{
			Transport: rt,
			Timeout:   cfg.Timeout(),
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return errors.New("redirects are not followed")
			},
		},
	}, nil
}

// Client calls one provider webhook.
type Client struct {
	url              string
	client           *http.Client
	maxResponseBytes int64
}

// URL returns the endpoint this client calls, for logging and error messages.
func (c *Client) URL() string {
	return c.url
}

// Call POSTs the review and returns the webhook's response.
//
// There is deliberately no retry: the webhook is authoritative on every request
// and retrying an unknown-outcome call is the client's business, not kcp's.
func (c *Client) Call(ctx context.Context, review *ephemeralv1alpha1.EphemeralReview) (*ephemeralv1alpha1.EphemeralResponse, error) {
	body, err := json.Marshal(review)
	if err != nil {
		return nil, fmt.Errorf("marshalling review: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("calling webhook %s: %w", c.url, err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, c.maxResponseBytes))
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("webhook %s returned HTTP %d, expected 200", c.url, resp.StatusCode)
	}

	// LimitReader with one extra byte so an oversized body is detected rather
	// than silently truncated into a parse error.
	raw, err := io.ReadAll(io.LimitReader(resp.Body, c.maxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("reading response from webhook %s: %w", c.url, err)
	}
	if int64(len(raw)) > c.maxResponseBytes {
		return nil, fmt.Errorf("response from webhook %s exceeds %d bytes", c.url, c.maxResponseBytes)
	}

	out := &ephemeralv1alpha1.EphemeralReview{}
	if err := json.Unmarshal(raw, out); err != nil {
		return nil, fmt.Errorf("decoding response from webhook %s: %w", c.url, err)
	}
	if out.Response == nil {
		return nil, fmt.Errorf("response from webhook %s has no response field", c.url)
	}
	if out.Response.UID != review.Request.UID {
		return nil, fmt.Errorf("response from webhook %s has uid %q, expected %q",
			c.url, out.Response.UID, review.Request.UID)
	}

	return out.Response, nil
}

// denyPrivateAddresses rejects connections to addresses that a provider webhook
// has no business living on. It runs after DNS resolution, so a hostname that
// resolves to a private address is caught too.
func denyPrivateAddresses(network, address string, _ syscall.RawConn) error {
	switch network {
	case "tcp", "tcp4", "tcp6":
	default:
		return fmt.Errorf("network %q is not allowed for webhook endpoints", network)
	}

	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("cannot parse webhook address %q: %w", address, err)
	}

	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("webhook address %q did not resolve to an IP", address)
	}

	switch {
	case ip.IsLoopback():
		return fmt.Errorf("webhook address %s is a loopback address", ip)
	case ip.IsLinkLocalUnicast(), ip.IsLinkLocalMulticast():
		return fmt.Errorf("webhook address %s is a link-local address", ip)
	case ip.IsPrivate():
		return fmt.Errorf("webhook address %s is a private address", ip)
	case ip.IsUnspecified():
		return fmt.Errorf("webhook address %s is unspecified", ip)
	case ip.IsInterfaceLocalMulticast(), ip.IsMulticast():
		return fmt.Errorf("webhook address %s is a multicast address", ip)
	}

	return nil
}
