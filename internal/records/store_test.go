// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Willem Mouton

package records

import (
	"testing"

	"github.com/miekg/dns"
)

func mustRecord(t *testing.T, name, rtype, value string) Record {
	t.Helper()
	r, err := New(name, rtype, value, 0)
	if err != nil {
		t.Fatalf("New(%q, %q, %q): %v", name, rtype, value, err)
	}
	return r
}

func TestMatchPattern(t *testing.T) {
	tests := []struct {
		pattern string
		name    string
		want    bool
	}{
		{"example.com", "example.com", true},
		{"example.com", "www.example.com", false}, // an exact name is not a suffix match
		{"*.example.com", "www.example.com", true},
		{"*.example.com", "a.b.example.com", true}, // leading * spans labels
		{"*.example.com", "example.com", false},    // and needs at least one
		{"*", "anything.at.all", true},
		{"*", "com", true},
		{"a.*.example.com", "a.b.example.com", true},
		{"a.*.example.com", "a.example.com", false}, // inner * is exactly one label
		{"a.*.example.com", "a.b.c.example.com", false},
		{"example.com", "example.org", false},
		{"EXAMPLE.com", "example.com", true}, // patterns are folded on load
	}

	for _, tc := range tests {
		_, got := matchPattern(NormaliseName(tc.pattern), reverseLabels(tc.name))
		if got != tc.want {
			t.Errorf("matchPattern(%q, %q) = %v, want %v", tc.pattern, tc.name, got, tc.want)
		}
	}
}

func TestLookupPrefersMostSpecific(t *testing.T) {
	s := NewStore(60)
	s.Add(mustRecord(t, "*", "A", "1.1.1.1"))
	s.Add(mustRecord(t, "*.dev.local", "A", "2.2.2.2"))
	s.Add(mustRecord(t, "api.dev.local", "A", "3.3.3.3"))

	tests := map[string]string{
		"api.dev.local":    "3.3.3.3",
		"web.dev.local":    "2.2.2.2",
		"v2.api.dev.local": "2.2.2.2",
		"example.com":      "1.1.1.1",
	}

	for qname, want := range tests {
		got := s.Lookup(qname, dns.TypeA)
		if len(got) != 1 {
			t.Fatalf("Lookup(%q): got %d records, want 1", qname, len(got))
		}
		if got[0].Value != want {
			t.Errorf("Lookup(%q) = %s, want %s", qname, got[0].Value, want)
		}
	}
}

func TestLookupReturnsAllValuesForWinningPattern(t *testing.T) {
	s := NewStore(60)
	s.Add(mustRecord(t, "api.dev.local", "A", "10.0.0.1"))
	s.Add(mustRecord(t, "api.dev.local", "A", "10.0.0.2"))
	s.Add(mustRecord(t, "*.dev.local", "A", "127.0.0.1"))

	got := s.Lookup("api.dev.local", dns.TypeA)
	if len(got) != 2 {
		t.Fatalf("got %d records, want 2", len(got))
	}
	for _, r := range got {
		if r.Name != "api.dev.local" {
			t.Errorf("unexpected record from less specific pattern: %s", r)
		}
	}
}

func TestLookupTypeIsolation(t *testing.T) {
	s := NewStore(60)
	s.Add(mustRecord(t, "dev.local", "A", "10.0.0.1"))

	if got := s.Lookup("dev.local", dns.TypeAAAA); len(got) != 0 {
		t.Errorf("AAAA lookup matched an A record: %v", got)
	}
	if !s.Knows("dev.local") {
		t.Error("Knows() should be true for a name with any record")
	}
	if s.Knows("other.local") {
		t.Error("Knows() should be false for an unknown name")
	}
}

func TestTypesForANY(t *testing.T) {
	s := NewStore(60)
	s.Add(mustRecord(t, "dev.local", "A", "10.0.0.1"))
	s.Add(mustRecord(t, "dev.local", "TXT", "hello"))
	s.Add(mustRecord(t, "other.local", "MX", "mail.other.local"))

	got := s.TypesFor("dev.local")
	if len(got) != 2 {
		t.Fatalf("TypesFor = %v, want 2 types", got)
	}
	if got[0] != dns.TypeA || got[1] != dns.TypeTXT {
		t.Errorf("TypesFor = %v, want [A TXT] in sorted order", got)
	}
}

func TestFilter(t *testing.T) {
	only := NewFilter(FilterOnly, []string{"dev.local", "*.dev.local"})
	if !only.ShouldFake("api.dev.local") {
		t.Error("only: listed name should be faked")
	}
	if only.ShouldFake("example.com") {
		t.Error("only: unlisted name should be proxied")
	}

	except := NewFilter(FilterExcept, []string{"github.com", "*.github.com"})
	if except.ShouldFake("github.com") {
		t.Error("except: listed name should be proxied")
	}
	if !except.ShouldFake("example.com") {
		t.Error("except: unlisted name should be faked")
	}

	none := NewFilter(FilterNone, nil)
	if !none.ShouldFake("anything") {
		t.Error("no filter should fake everything")
	}
	// An empty pattern list degrades to no filtering rather than blocking all.
	if empty := NewFilter(FilterOnly, nil); !empty.ShouldFake("anything") {
		t.Error("empty only-list should not block everything")
	}
}
