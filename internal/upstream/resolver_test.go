// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Willem Mouton

package upstream

import "testing"

func TestParseServer(t *testing.T) {
	tests := []struct {
		in      string
		addr    string
		network string
		tlsName string
	}{
		{"1.1.1.1", "1.1.1.1:53", "udp", ""},
		{"1.1.1.1:5353", "1.1.1.1:5353", "udp", ""},
		{"2001:4860:4860::8888", "[2001:4860:4860::8888]:53", "udp", ""},
		{"[2001:4860:4860::8888]:53", "[2001:4860:4860::8888]:53", "udp", ""},
		{"udp://9.9.9.9:53", "9.9.9.9:53", "udp", ""},
		{"tcp://9.9.9.9:53", "9.9.9.9:53", "tcp", ""},
		{"tls://1.1.1.1:853#cloudflare-dns.com", "1.1.1.1:853", "tcp-tls", "cloudflare-dns.com"},
		{"tls://one.one.one.one", "one.one.one.one:853", "tcp-tls", "one.one.one.one"},
		{"dns.example.com:5353", "dns.example.com:5353", "udp", ""},
		// Legacy HOST#PORT#PROTOCOL syntax, so old invocations keep working.
		{"8.8.8.8#53", "8.8.8.8:53", "udp", ""},
		{"4.2.2.1#53#tcp", "4.2.2.1:53", "tcp", ""},
	}

	for _, tc := range tests {
		got, err := ParseServer(tc.in)
		if err != nil {
			t.Errorf("ParseServer(%q): %v", tc.in, err)
			continue
		}
		if got.Addr != tc.addr || got.Net != tc.network || got.TLSName != tc.tlsName {
			t.Errorf("ParseServer(%q) = %+v, want addr=%s net=%s tls=%s",
				tc.in, got, tc.addr, tc.network, tc.tlsName)
		}
	}

	for _, bad := range []string{"", "   ", "http://1.1.1.1", "1.1.1.1#53#sctp", "a#b#c#d"} {
		if _, err := ParseServer(bad); err == nil {
			t.Errorf("ParseServer(%q) should have failed", bad)
		}
	}
}

func TestNewRequiresAtLeastOneServer(t *testing.T) {
	if _, err := New(nil, 0); err == nil {
		t.Error("expected an error with no upstreams")
	}
	if _, err := New([]string{"", "  "}, 0); err == nil {
		t.Error("blank entries should not count as servers")
	}
}

func TestServerString(t *testing.T) {
	tests := map[string]string{
		"1.1.1.1":                              "1.1.1.1:53",
		"tcp://9.9.9.9:53":                     "tcp://9.9.9.9:53",
		"tls://1.1.1.1:853#cloudflare-dns.com": "tls://1.1.1.1:853#cloudflare-dns.com",
	}
	for in, want := range tests {
		srv, err := ParseServer(in)
		if err != nil {
			t.Fatal(err)
		}
		if got := srv.String(); got != want {
			t.Errorf("ParseServer(%q).String() = %q, want %q", in, got, want)
		}
	}
}
