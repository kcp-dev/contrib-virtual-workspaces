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
	"github.com/kcp-dev/contrib-virtual-workspaces/pkg/clicontext"
)

// Selection is the tenant and project the user is currently working in:
// what `use` picks, and what every command that needs a workspace falls
// back to when no flag names one.
//
// It is a view onto the shared CLI context in ~/.kube, which also holds
// the server `login` learned — one file, so a command never has to be
// told twice where anything is.
type Selection struct {
	Tenant  string
	Project string
	// URL is the project's workspace, as the virtual workspace reported
	// it, so commands that only need somewhere to talk to do not have to
	// ask again.
	URL string
}

// String renders the selection the way the CLI prints it.
func (s Selection) String() string {
	if s.Project == "" {
		return s.Tenant
	}
	return s.Tenant + "/" + s.Project
}

// Empty reports whether nothing has been selected yet.
func (s Selection) Empty() bool { return s.Tenant == "" }

// CurrentSelection reads the selection from the shared context.
func CurrentSelection() Selection {
	c := clicontext.Load()
	return Selection{Tenant: c.Tenant, Project: c.Project, URL: c.WorkspaceURL}
}

// WriteSelection stores the selection, leaving the rest of the context —
// the server, the TLS settings — as it was.
func WriteSelection(s Selection) error {
	return clicontext.Update(func(c *clicontext.Context) {
		c.Tenant, c.Project, c.WorkspaceURL = s.Tenant, s.Project, s.URL
	})
}

// ClearSelection forgets where the user was working, but keeps them
// logged in and keeps the server: they chose a project, not a cluster.
func ClearSelection() error {
	return clicontext.Update(func(c *clicontext.Context) {
		c.Tenant, c.Project, c.WorkspaceURL = "", "", ""
	})
}

// storedServiceURL is where a virtual workspace answers on the server
// `login --url` stored, or "" when no server is known yet — in which case
// the flag is required and says so.
func storedServiceURL(name string) string {
	return clicontext.Load().ServiceURL(name)
}

// storedTLS reports the TLS settings saved with the server, so a
// development setup does not need --insecure-skip-tls-verify on every
// command.
func storedTLS() (caFile string, insecure bool) {
	c := clicontext.Load()
	return c.CertificateAuthority, c.Insecure
}
