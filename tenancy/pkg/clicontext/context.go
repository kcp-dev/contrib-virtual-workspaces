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

// Package clicontext holds what the CLI remembers between commands: the
// server it talks to, and where the user is working.
//
// It lives beside the kubeconfig, in ~/.kube, because it is the same kind
// of thing — the local note about which cluster you mean — and because
// anyone debugging a CLI looks there first.
//
// It belongs to the tenancy CLI because that is where it is written:
// `login` learns the server, `use` picks the project. Other components'
// commands are handed what they need from it by whichever binary composes
// them (contribctl), the way the marketplace's `enable` is handed a
// workspace resolver — so the components still never import each other,
// and a component's CLI on its own needs nothing but its flags.
//
// Nothing here authorizes anything. It is a convenience that saves
// repeating flags — kcp decides what the caller may do.
package clicontext

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// EnvPath overrides where the context is stored.
const EnvPath = "CONTRIBCTL_CONTEXT"

// Context is the CLI's memory between invocations.
type Context struct {
	// Server is the front-proxy's base URL, e.g.
	// https://kcp.example.com:8443. Every virtual workspace hangs off it
	// under /services/<name>, so one address is all a client needs.
	Server string `json:"server,omitempty"`
	// Tenant and Project are what `use` selected.
	Tenant  string `json:"tenant,omitempty"`
	Project string `json:"project,omitempty"`
	// WorkspaceURL is the selected project's own workspace, as the
	// tenancy virtual workspace reported it.
	WorkspaceURL string `json:"workspaceURL,omitempty"`
	// Insecure records that the servers' certificates are not verified,
	// so a development setup does not need the flag on every command.
	Insecure bool `json:"insecureSkipTLSVerify,omitempty"`
	// CertificateAuthority is the CA bundle that verifies them, when they
	// are verified.
	CertificateAuthority string `json:"certificateAuthority,omitempty"`
}

// Where returns the path the context is read from and written to.
func Where() string {
	if p := os.Getenv(EnvPath); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".kube", "contribctl.json")
}

// Load reads the stored context. A missing or unreadable file is an empty
// context, not an error: every caller has to cope with "nothing chosen
// yet" anyway, and a corrupt file should not make the CLI unusable.
func Load() Context {
	path := Where()
	if path == "" {
		return Context{}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return Context{}
	}
	var c Context
	if err := json.Unmarshal(raw, &c); err != nil {
		return Context{}
	}
	return c
}

// Save writes the context, creating ~/.kube if it is not there yet.
func Save(c Context) error {
	path := Where()
	if path == "" {
		return fmt.Errorf("no home directory could be determined; set %s", EnvPath)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("serialize the context: %w", err)
	}
	if err := os.WriteFile(path, append(raw, '\n'), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// Update reads the context, applies mutate, and writes it back, so a
// command changes only the field it owns.
func Update(mutate func(*Context)) error {
	c := Load()
	mutate(&c)
	return Save(c)
}

// ServiceURL is where a virtual workspace answers on the stored server:
// the front-proxy routes /services/<name> to it.
//
// Empty when no server is known, which is the caller's cue to ask for
// one — the CLI cannot guess an address.
func (c Context) ServiceURL(name string) string {
	if c.Server == "" {
		return ""
	}
	return strings.TrimSuffix(c.Server, "/") + "/services/" + name
}

// Selection renders where the user is working, for messages.
func (c Context) Selection() string {
	switch {
	case c.Tenant == "":
		return ""
	case c.Project == "":
		return c.Tenant
	default:
		return c.Tenant + "/" + c.Project
	}
}
