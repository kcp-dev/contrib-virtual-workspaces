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

// Package config holds the operator-supplied registry of webhook-backed
// resources.
//
// Out of tree, providers cannot register a webhook themselves: kcp's cache
// server only replicates a hardcoded set of kinds, so a provider-owned CRD
// carrying webhook configuration would never reach the shard resolving the
// request. Registration therefore goes through this file, owned by whoever runs
// the virtual workspace. That is also the de facto authorization gate for the
// feature.
package config

import (
	"fmt"
	"net/url"
	"os"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/yaml"
)

const (
	// Kind is the kind of the configuration document.
	Kind = "EphemeralWebhookConfiguration"

	// APIVersion is the apiVersion of the configuration document.
	APIVersion = "ephemeral.contrib.kcp.io/v1alpha1"

	// DefaultTimeoutSeconds bounds how long the virtual workspace waits for a
	// webhook response when the entry does not say.
	DefaultTimeoutSeconds int32 = 10

	// MaxTimeoutSeconds is the hard cap on webhook timeouts. A slow webhook
	// holds a request goroutine for its whole duration, so this is not
	// negotiable per entry.
	MaxTimeoutSeconds int32 = 30
)

// Configuration is the root of the configuration file.
type Configuration struct {
	metav1.TypeMeta `json:",inline"`

	// Resources enumerates the webhook-backed resources this virtual workspace
	// serves.
	Resources []ResourceConfiguration `json:"resources"`
}

// ResourceConfiguration binds one resource of one APIExport to one webhook.
type ResourceConfiguration struct {
	// Export identifies the APIExport that exposes the resource. The virtual
	// workspace reads the APIResourceSchema referenced by the export's
	// spec.resources entry for Group/Resource, so the schema does not have to be
	// named here.
	Export ExportReference `json:"export"`

	// Group is the API group of the served resource.
	Group string `json:"group"`

	// Resource is the plural resource name of the served resource.
	Resource string `json:"resource"`

	// Webhook describes the endpoint that answers requests for this resource.
	Webhook WebhookConfiguration `json:"webhook"`
}

// ExportReference points at an APIExport by workspace path and name.
type ExportReference struct {
	// Path is the logical cluster path the APIExport lives in, e.g.
	// "root:providers:s3".
	Path string `json:"path"`

	// Name is the name of the APIExport.
	Name string `json:"name"`
}

func (e ExportReference) String() string {
	return e.Path + "|" + e.Name
}

// WebhookConfiguration describes how to reach a provider's webhook.
type WebhookConfiguration struct {
	// URL is the HTTPS endpoint to call. Plain http is rejected. There is no
	// service variant: a Service in a logical cluster has no backing endpoints.
	URL string `json:"url"`

	// CABundleFile is a PEM file used to verify the endpoint's serving
	// certificate. Empty means the system trust store.
	//
	// +optional
	CABundleFile string `json:"caBundleFile,omitempty"`

	// ClientCertFile and ClientKeyFile are the client certificate this virtual
	// workspace presents to the webhook.
	//
	// The webhook is told who the user is via request.userInfo, which puts it in
	// the position an aggregated API server is in: it must know the assertion
	// came from a party entitled to make it. Without client authentication,
	// anyone who can reach the endpoint asserts arbitrary identity. Configuring
	// them is strongly recommended and providers should verify the chain, not
	// merely require a certificate.
	//
	// +optional
	ClientCertFile string `json:"clientCertFile,omitempty"`
	// +optional
	ClientKeyFile string `json:"clientKeyFile,omitempty"`

	// TimeoutSeconds bounds how long to wait for a response. Defaults to 10,
	// capped at 30.
	//
	// +optional
	TimeoutSeconds int32 `json:"timeoutSeconds,omitempty"`
}

// Timeout returns the effective timeout for this webhook.
func (w WebhookConfiguration) Timeout() time.Duration {
	seconds := w.TimeoutSeconds
	if seconds <= 0 {
		seconds = DefaultTimeoutSeconds
	}
	if seconds > MaxTimeoutSeconds {
		seconds = MaxTimeoutSeconds
	}
	return time.Duration(seconds) * time.Second
}

// Load reads and validates a configuration file.
func Load(path string) (*Configuration, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}

	cfg := &Configuration{}
	if err := yaml.UnmarshalStrict(raw, cfg); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}

	if errs := cfg.Validate(); len(errs) > 0 {
		return nil, fmt.Errorf("invalid configuration %s: %w", path, errs.ToAggregate())
	}

	return cfg, nil
}

// Validate checks the configuration for internal consistency.
func (c *Configuration) Validate() field.ErrorList {
	var errs field.ErrorList

	if c.APIVersion != "" && c.APIVersion != APIVersion {
		errs = append(errs, field.NotSupported(field.NewPath("apiVersion"), c.APIVersion, []string{APIVersion}))
	}
	if c.Kind != "" && c.Kind != Kind {
		errs = append(errs, field.NotSupported(field.NewPath("kind"), c.Kind, []string{Kind}))
	}
	if len(c.Resources) == 0 {
		errs = append(errs, field.Required(field.NewPath("resources"), "at least one resource must be configured"))
	}

	seen := sets.New[string]()
	for i, r := range c.Resources {
		path := field.NewPath("resources").Index(i)

		if r.Export.Path == "" {
			errs = append(errs, field.Required(path.Child("export", "path"), ""))
		}
		if r.Export.Name == "" {
			errs = append(errs, field.Required(path.Child("export", "name"), ""))
		}
		if r.Group == "" {
			errs = append(errs, field.Required(path.Child("group"), "the core group is not supported for ephemeral resources"))
		}
		if r.Resource == "" {
			errs = append(errs, field.Required(path.Child("resource"), ""))
		}

		key := r.Export.String() + "|" + r.Group + "|" + r.Resource
		if seen.Has(key) {
			errs = append(errs, field.Duplicate(path, key))
		}
		seen.Insert(key)

		errs = append(errs, validateWebhook(r.Webhook, path.Child("webhook"))...)
	}

	return errs
}

func validateWebhook(w WebhookConfiguration, path *field.Path) field.ErrorList {
	var errs field.ErrorList

	switch {
	case w.URL == "":
		errs = append(errs, field.Required(path.Child("url"), ""))
	default:
		u, err := url.Parse(w.URL)
		switch {
		case err != nil:
			errs = append(errs, field.Invalid(path.Child("url"), w.URL, err.Error()))
		case u.Scheme != "https":
			errs = append(errs, field.Invalid(path.Child("url"), w.URL, "must use the https scheme"))
		case u.Host == "":
			errs = append(errs, field.Invalid(path.Child("url"), w.URL, "must have a host"))
		case u.RawQuery != "":
			errs = append(errs, field.Invalid(path.Child("url"), w.URL, "must not have a query string"))
		case u.Fragment != "":
			errs = append(errs, field.Invalid(path.Child("url"), w.URL, "must not have a fragment"))
		case u.User != nil:
			errs = append(errs, field.Invalid(path.Child("url"), w.URL, "must not have user info"))
		}
	}

	if (w.ClientCertFile == "") != (w.ClientKeyFile == "") {
		errs = append(errs, field.Invalid(path, "", "clientCertFile and clientKeyFile must be set together"))
	}

	if w.TimeoutSeconds < 0 {
		errs = append(errs, field.Invalid(path.Child("timeoutSeconds"), w.TimeoutSeconds, "must not be negative"))
	}
	if w.TimeoutSeconds > MaxTimeoutSeconds {
		errs = append(errs, field.Invalid(path.Child("timeoutSeconds"), w.TimeoutSeconds,
			fmt.Sprintf("must not exceed %d", MaxTimeoutSeconds)))
	}

	return errs
}
