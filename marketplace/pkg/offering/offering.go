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

// Package offering reads an APIExport as a catalog card.
//
// An offering has no object of its own: a producer publishes by annotating
// their APIExport, and the export's own permission claims are the claim
// surface a consumer accepts. This package is the single place that knows
// how to read that, so the operator (which grants `bind`) and the
// directory (which serves the catalog) cannot disagree about what is
// published or about what enabling would accept.
package offering

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"

	marketplacev1alpha1 "github.com/kcp-dev/contrib-virtual-workspaces/marketplace/sdk/apis/marketplace/v1alpha1"
)

// Card is one offering, as read off the APIExport that is it.
type Card struct {
	// DisplayName falls back to the export's own name.
	DisplayName string
	Description string
	// IdentityHash is the export's identity, from its status. It is empty
	// until kcp has reconciled the export, which is one of the two reasons
	// an offering is not yet Ready.
	IdentityHash string
	// Claims is the export's permission claims, projected.
	Claims []marketplacev1alpha1.OfferingClaim
	// Revision is a digest of Claims.
	Revision string
	// DocsURL is the producer's documentation link, if any.
	DocsURL string
	// InstructionsRef names the ConfigMap holding the producer's "how to
	// use this", in the export's own workspace. Empty means none.
	InstructionsRef string
}

// Published reports whether an export opts into the catalog.
//
// Only the exact string "true" publishes. An annotation present with any
// other value is not "sort of published": a typo should leave an export
// off the catalog rather than on it, because being on it is what grants
// `bind` to strangers.
func Published(export *apisv1alpha2.APIExport) bool {
	if export == nil {
		return false
	}
	return export.Annotations[marketplacev1alpha1.AnnotationPublish] == "true"
}

// From reads the card off a published export.
func From(export *apisv1alpha2.APIExport) Card {
	card := Card{
		DisplayName:     export.Annotations[marketplacev1alpha1.AnnotationDisplayName],
		Description:     export.Annotations[marketplacev1alpha1.AnnotationDescription],
		IdentityHash:    export.Status.IdentityHash,
		Claims:          Claims(export.Spec.PermissionClaims),
		DocsURL:         export.Annotations[marketplacev1alpha1.AnnotationDocsURL],
		InstructionsRef: export.Annotations[marketplacev1alpha1.AnnotationInstructions],
	}
	if card.DisplayName == "" {
		card.DisplayName = export.Name
	}
	card.Revision = Revision(card.Claims)
	return card
}

// Claims projects an export's permission claims into the catalog's view of
// them. Nothing is validated, because there is nothing to validate against:
// the export declaring a claim is the claim existing.
func Claims(declared []apisv1alpha2.PermissionClaim) []marketplacev1alpha1.OfferingClaim {
	if len(declared) == 0 {
		return nil
	}
	out := make([]marketplacev1alpha1.OfferingClaim, 0, len(declared))
	for _, claim := range declared {
		verbs := append([]string(nil), claim.Verbs...)
		sort.Strings(verbs)
		projected := marketplacev1alpha1.OfferingClaim{
			Group:        claim.Group,
			Resource:     claim.Resource,
			Verbs:        verbs,
			IdentityHash: claim.IdentityHash,
		}
		// The producer's suggested scope for this claim. Only matchLabels
		// is carried: it is what a consumer can act on when authoring the
		// binding, and an unset selector already means "every object",
		// which is the default a binding takes anyway.
		if sel := claim.DefaultSelector; sel != nil && len(sel.MatchLabels) > 0 {
			projected.MatchLabels = make(map[string]string, len(sel.MatchLabels))
			for k, v := range sel.MatchLabels {
				projected.MatchLabels[k] = v
			}
		}
		out = append(out, projected)
	}
	// Sorted so that reordering claims on the export is not a change of
	// consent — only the content is.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Group != out[j].Group {
			return out[i].Group < out[j].Group
		}
		return out[i].Resource < out[j].Resource
	})
	return out
}

// Revision is a digest of the claim surface: the same claims always give
// the same revision, and any change gives a different one.
//
// This is what a consumer compares against the revision they accepted, to
// see whether the consent form moved under them. A digest rather than a
// counter because there is no object left to keep a counter on — and
// because a counter would restart at a different number every time a
// controller lost its memory of it, which is precisely when a consumer
// most wants the comparison to still mean something.
func Revision(claims []marketplacev1alpha1.OfferingClaim) string {
	if len(claims) == 0 {
		return ""
	}
	var b strings.Builder
	for _, c := range claims {
		fmt.Fprintf(&b, "%s/%s#%s:%s", c.Group, c.Resource, c.IdentityHash, strings.Join(c.Verbs, ","))
		if len(c.MatchLabels) > 0 {
			keys := make([]string, 0, len(c.MatchLabels))
			for k := range c.MatchLabels {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				fmt.Fprintf(&b, "|%s=%s", k, c.MatchLabels[k])
			}
		}
		b.WriteByte(';')
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])[:12]
}
