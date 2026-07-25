// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Willem Mouton

package records

import (
	"strings"
	"testing"

	"github.com/miekg/dns"
)

// TestNormaliseAcceptsLegacyValues checks that every value format used by the
// legacy INI format still parses, so an imported config behaves the same.
func TestNormaliseAcceptsLegacyValues(t *testing.T) {
	tests := []struct {
		rtype string
		value string
	}{
		{"A", "192.0.2.1"},
		{"AAAA", "2001:db8::1"},
		{"MX", "mail.fake.com"},
		{"NS", "ns.fake.com"},
		{"CNAME", "www.fake.com"},
		{"TXT", "fake message"},
		{"PTR", "fake.com"},
		{"SOA", "ns.fake.com. hostmaster.fake.com. 1 10800 3600 604800 3600"},
		{"NAPTR", "100 10 U E2U+sip !^.*$!sip:customer-service@fake.com! ."},
		{"SRV", "0 5 5060 sipserver.fake.com"},
		{"DNSKEY", "256 3 5 AQPSKmynfzW4kyBv015MUG2DeIQ3Cbl+BBZH4b/0PY1kxkmvHjcZc8nokfzj31GajIQKY+5CptLr3buXA10hWqTkF7H6RfoRqXQeogmMHfpftf6zMv1LyBUgia7za6ZEzOJBOztyvhjL742iU/TpPSEDhm2SNKLijfUppn1UaNvv4w=="},
		{"RRSIG", "A 5 3 86400 20030322173103 20030220173103 2642 example.com. oJB1W6WNGv+ldvQ3WDG0MQkg5IEhjRip8WTrPYGv07h108dUKGMeDPKijVCHX3DDKdfb+v6oB9wfuh3DTJXUAfI/M0zmO/zz8bW0Rznl8O3tGNazPwQKkRN20XPXV6nwwfoXmJQbsLNrLfkGJ5D6fwFm8nN+6pBzeDQfsS3Ap3o="},
		// Types the legacy format never supported, which now work for free.
		{"CAA", "0 issue letsencrypt.org"},
		{"HTTPS", `1 . alpn="h2,h3"`},
	}

	for _, tc := range tests {
		if _, err := New("test.example.com", tc.rtype, tc.value, 0); err != nil {
			t.Errorf("%s %q: %v", tc.rtype, tc.value, err)
		}
	}
}

func TestRecordRRUsesQueriedName(t *testing.T) {
	r, err := New("*.dev.local", "A", "127.0.0.1", 0)
	if err != nil {
		t.Fatal(err)
	}

	rr, err := r.RR("api.dev.local", 60)
	if err != nil {
		t.Fatal(err)
	}
	if got := rr.Header().Name; got != "api.dev.local." {
		t.Errorf("owner name = %q, want the queried name", got)
	}
	if got := rr.Header().Ttl; got != 60 {
		t.Errorf("ttl = %d, want 60", got)
	}
	if got := rr.(*dns.A).A.String(); got != "127.0.0.1" {
		t.Errorf("address = %s", got)
	}
}

func TestRecordTTLPrecedence(t *testing.T) {
	r, err := New("dev.local", "A", "127.0.0.1", 300)
	if err != nil {
		t.Fatal(err)
	}
	rr, err := r.RR("dev.local", 60)
	if err != nil {
		t.Fatal(err)
	}
	if rr.Header().Ttl != 300 {
		t.Errorf("per-record TTL should win, got %d", rr.Header().Ttl)
	}
}

func TestLongTXTIsSplitIntoChunks(t *testing.T) {
	long := strings.Repeat("a", 600)
	r, err := New("dev.local", "TXT", long, 0)
	if err != nil {
		t.Fatal(err)
	}
	rr, err := r.RR("dev.local", 60)
	if err != nil {
		t.Fatal(err)
	}
	txt := rr.(*dns.TXT).Txt
	if len(txt) != 3 {
		t.Fatalf("got %d character-strings, want 3", len(txt))
	}
	if strings.Join(txt, "") != long {
		t.Error("round-tripped TXT does not match the original")
	}
}

func TestRejectsMismatchedAddressFamily(t *testing.T) {
	if _, err := New("dev.local", "A", "::1", 0); err == nil {
		t.Error("an IPv6 address should not be accepted as an A record")
	}
	if _, err := New("dev.local", "AAAA", "127.0.0.1", 0); err == nil {
		t.Error("an IPv4 address should not be accepted as an AAAA record")
	}
	if _, err := New("dev.local", "A", "not-an-ip", 0); err == nil {
		t.Error("a hostname should not be accepted as an A record")
	}
	if _, err := New("dev.local", "NOPE", "x", 0); err == nil {
		t.Error("an unknown record type should be rejected")
	}
}

func TestMXAcceptsBareHostAndPreference(t *testing.T) {
	bare, err := New("dev.local", "MX", "mail.dev.local", 0)
	if err != nil {
		t.Fatal(err)
	}
	rr, _ := bare.RR("dev.local", 60)
	if mx := rr.(*dns.MX); mx.Preference != 10 || mx.Mx != "mail.dev.local." {
		t.Errorf("bare MX = %d %q, want 10 mail.dev.local.", mx.Preference, mx.Mx)
	}

	withPref, err := New("dev.local", "MX", "20 backup.dev.local", 0)
	if err != nil {
		t.Fatal(err)
	}
	rr, _ = withPref.RR("dev.local", 60)
	if mx := rr.(*dns.MX); mx.Preference != 20 {
		t.Errorf("preference = %d, want 20", mx.Preference)
	}
}

func TestNormaliseName(t *testing.T) {
	tests := map[string]string{
		"Example.COM.": "example.com",
		"example.com":  "example.com",
		"  x.local  ":  "x.local",
		".":            "",
		"*.Dev.Local.": "*.dev.local",
	}
	for in, want := range tests {
		if got := NormaliseName(in); got != want {
			t.Errorf("NormaliseName(%q) = %q, want %q", in, got, want)
		}
	}
}
