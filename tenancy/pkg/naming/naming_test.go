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

package naming

import (
	"regexp"
	"strings"
	"testing"
)

// dnsLabel is what kcp accepts as a workspace name.
var dnsLabel = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

func TestSlugify(t *testing.T) {
	cases := map[string]struct {
		in   string
		want string
	}{
		"plain":               {"acme", "acme"},
		"spaces become dash":  {"Acme Corp", "acme-corp"},
		"case folds":          {"ACME", "acme"},
		"punctuation folds":   {"Acme, Inc. (EU)", "acme-inc-eu"},
		"unicode drops":       {"Åcme ütf", "cme-tf"},
		"emoji drops":         {"🚀 rocket", "rocket"},
		"only junk":           {"!!!", ""},
		"empty":               {"", ""},
		"inner runs collapse": {"a  --  b", "a-b"},
		"leading junk":        {"--acme", "acme"},
		"long truncates":      {strings.Repeat("a", 100), strings.Repeat("a", 54)},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := Slugify(tc.in)
			if got != tc.want {
				t.Errorf("Slugify(%q) = %q, want %q", tc.in, got, tc.want)
			}
			if got != "" && !dnsLabel.MatchString(got) {
				t.Errorf("Slugify(%q) = %q is not a DNS label", tc.in, got)
			}
		})
	}
}

func TestStrategiesProduceValidNames(t *testing.T) {
	hostile := []string{
		"", "   ", "...", "🔥🔥🔥", "a", "A Very Long Display Name That Goes On And On " +
			"Past Any Reasonable Length Limit For A DNS Label Somehow",
		"кириллица", "space  inside", "-leading-dash", "trailing-dash-", "UPPER",
	}
	for _, strategyName := range []string{StrategySlug, StrategyUID} {
		s, err := ForName(strategyName)
		if err != nil {
			t.Fatalf("ForName(%q): %v", strategyName, err)
		}
		for _, display := range hostile {
			candidates := s.Propose(display, "8f6c4c6e-3049-4d47-9151-6d1e4a2f0a77")
			if len(candidates) == 0 {
				t.Errorf("%s.Propose(%q) returned no candidates", strategyName, display)
			}
			for _, c := range candidates {
				if !dnsLabel.MatchString(c) {
					t.Errorf("%s.Propose(%q) proposed %q, not a DNS label", strategyName, display, c)
				}
				if len(c) > 63 {
					t.Errorf("%s.Propose(%q) proposed %q, longer than a DNS label", strategyName, display, c)
				}
			}
		}
	}
}

func TestProposeIsDeterministic(t *testing.T) {
	s, _ := ForName(StrategySlug)
	a := s.Propose("Acme Corp", "uid-1")
	b := s.Propose("Acme Corp", "uid-1")
	if strings.Join(a, ",") != strings.Join(b, ",") {
		t.Errorf("Propose is not deterministic: %v vs %v", a, b)
	}
}

func TestCollidingDisplayNamesDivergeOnFallback(t *testing.T) {
	s, _ := ForName(StrategySlug)
	a := s.Propose("Acme", "uid-1")
	b := s.Propose("Acme", "uid-2")
	if a[0] != b[0] {
		t.Fatalf("first candidates should collide by construction: %q vs %q", a[0], b[0])
	}
	if a[len(a)-1] == b[len(b)-1] {
		t.Errorf("fallback candidates must differ per UID, both were %q", a[len(a)-1])
	}
}

func TestUnknownStrategyIsAnError(t *testing.T) {
	if _, err := ForName("cute-animals"); err == nil {
		t.Error("expected an error for an unknown strategy")
	}
}
