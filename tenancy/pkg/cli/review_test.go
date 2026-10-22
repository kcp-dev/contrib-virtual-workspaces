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

package cli

import (
	"testing"

	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	tenancyv1alpha1 "github.com/kcp-dev/contrib-virtual-workspaces/tenancy/sdk/apis/tenancy/v1alpha1"
)

func TestDefaultContext(t *testing.T) {
	// Only projects get contexts at all — a tenant workspace is a record
	// store no member can enter — so this only ever chooses between
	// projects, preferring one the caller holds a role in.
	cases := map[string]struct {
		tenants  []tenancyv1alpha1.TenantClaim
		contexts []string
		want     string
	}{
		"the only project is the default": {
			tenants: []tenancyv1alpha1.TenantClaim{{
				Name:  "acme",
				Roles: []string{"admin"},
				Projects: []tenancyv1alpha1.ProjectClaim{
					{Name: "web", Roles: []string{"admin"}},
				},
			}},
			contexts: []string{"acme-web"},
			want:     "acme-web",
		},
		"a project with roles wins over one without": {
			tenants: []tenancyv1alpha1.TenantClaim{{
				Name: "acme",
				Projects: []tenancyv1alpha1.ProjectClaim{
					{Name: "attic"},
					{Name: "web", Roles: []string{"edit"}},
				},
			}},
			contexts: []string{"acme-attic", "acme-web"},
			want:     "acme-web",
		},
		"a project reached only by a tenant-wide grant still counts": {
			tenants: []tenancyv1alpha1.TenantClaim{{
				Name:  "acme",
				Roles: []string{"admin"}, // fans out; not repeated per project
				Projects: []tenancyv1alpha1.ProjectClaim{
					{Name: "web"},
				},
			}},
			contexts: []string{"acme-web"},
			want:     "acme-web",
		},
		"a project whose workspace has no endpoint is skipped": {
			tenants: []tenancyv1alpha1.TenantClaim{{
				Name: "acme",
				Projects: []tenancyv1alpha1.ProjectClaim{
					{Name: "pending", Roles: []string{"admin"}},
					{Name: "web", Roles: []string{"view"}},
				},
			}},
			// "pending" is still provisioning, so no context was written.
			contexts: []string{"acme-web"},
			want:     "acme-web",
		},
		"the first project of the first tenant, across tenants": {
			tenants: []tenancyv1alpha1.TenantClaim{
				{Name: "acme", Projects: []tenancyv1alpha1.ProjectClaim{
					{Name: "web", Roles: []string{"view"}},
				}},
				{Name: "globex", Projects: []tenancyv1alpha1.ProjectClaim{
					{Name: "api", Roles: []string{"admin"}},
				}},
			},
			contexts: []string{"acme-web", "globex-api"},
			want:     "acme-web",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			contexts := map[string]*clientcmdapi.Context{}
			for _, c := range tc.contexts {
				contexts[c] = clientcmdapi.NewContext()
			}
			if got := defaultContext(tc.tenants, contexts); got != tc.want {
				t.Errorf("defaultContext = %q, want %q", got, tc.want)
			}
		})
	}
}
