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

// Package naming turns human display names into kcp workspace names.
//
// Workspace names are DNS labels and display names are arbitrary UTF-8, so
// something has to give. A Strategy decides what: how a display name maps
// to a workspace name, and what to fall back to when two objects want the
// same one. Every strategy is a pure function of the object's display name
// and UID, so reconciling the same object always proposes the same names
// and never needs to store a decision anywhere.
package naming

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// maxNameLength is the longest workspace name a strategy proposes. DNS
// labels allow 63; staying below leaves room for the collision suffix.
const maxNameLength = 54

// suffixLength is how many hex characters of the UID hash the collision
// fallback appends.
const suffixLength = 8

// A Strategy proposes workspace names for an object. Propose returns
// candidates in preference order; the caller tries each until one is free
// or already owned by the same object. The last candidate embeds the
// object's UID and therefore cannot collide with a different object's
// candidates, so the list never runs out for a well-behaved caller.
type Strategy interface {
	// Name returns the strategy's flag value.
	Name() string
	// Propose returns candidate workspace names, most preferred first.
	// Every candidate is a valid DNS label. displayName may be arbitrary
	// UTF-8; uid must be stable and unique for the object's lifetime.
	Propose(displayName, uid string) []string
}

// ForName returns the strategy registered under name.
func ForName(name string) (Strategy, error) {
	switch name {
	case StrategySlug:
		return slugStrategy{}, nil
	case StrategyUID:
		return uidStrategy{}, nil
	default:
		return nil, fmt.Errorf("unknown naming strategy %q (known: %s, %s)", name, StrategySlug, StrategyUID)
	}
}

const (
	// StrategySlug names workspaces after the display name, slugified,
	// falling back to a UID-derived suffix on collision. The default.
	StrategySlug = "slug"
	// StrategyUID names workspaces after the object's UID alone. Opaque
	// but immune to display-name games; use it when display names are
	// user-controlled and cosmetic.
	StrategyUID = "uid"
)

type slugStrategy struct{}

func (slugStrategy) Name() string { return StrategySlug }

func (slugStrategy) Propose(displayName, uid string) []string {
	suffix := uidSuffix(uid)
	slug := Slugify(displayName)
	if slug == "" {
		// Nothing of the display name survived slugification; there is no
		// pretty candidate to try first.
		return []string{"ws-" + suffix}
	}
	return []string{slug, truncate(slug, maxNameLength-1-suffixLength) + "-" + suffix}
}

type uidStrategy struct{}

func (uidStrategy) Name() string { return StrategyUID }

func (uidStrategy) Propose(_, uid string) []string {
	return []string{"ws-" + uidSuffix(uid)}
}

// uidSuffix derives a short stable identifier from a UID. Hashing rather
// than slicing keeps it uniform even for UIDs that share a prefix.
func uidSuffix(uid string) string {
	sum := sha256.Sum256([]byte(uid))
	return hex.EncodeToString(sum[:])[:suffixLength]
}

// Slugify reduces an arbitrary display name to a DNS label: lowercase
// alphanumerics with single dashes between words, no leading or trailing
// dash, at most maxNameLength characters. Returns "" when nothing of the
// name survives.
func Slugify(displayName string) string {
	var b strings.Builder
	dashPending := false
	for _, r := range strings.ToLower(displayName) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			if dashPending && b.Len() > 0 {
				b.WriteByte('-')
			}
			dashPending = false
			b.WriteRune(r)
		default:
			// Every run of non-label characters collapses into one dash.
			dashPending = true
		}
	}
	return truncate(b.String(), maxNameLength)
}

// truncate shortens s to at most n bytes without leaving a trailing dash.
func truncate(s string, n int) string {
	if len(s) > n {
		s = s[:n]
	}
	return strings.TrimRight(s, "-")
}
