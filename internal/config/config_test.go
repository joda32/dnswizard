// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Willem Mouton

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/miekg/dns"
)

func writeTemp(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadFullConfig(t *testing.T) {
	path := writeTemp(t, "dnswizard.yaml", `
listen:
  - 127.0.0.1:5353
upstream:
  - tls://1.1.1.1:853#cloudflare-dns.com
ttl: 120
timeout: 5s
fallback: nxdomain
hosts:
  "*.dev.local": 127.0.0.1
  "api.dev.local":
    - 10.0.0.1
    - 10.0.0.2
records:
  - name: dev.local
    type: MX
    value: mail.dev.local
  - name: dev.local
    type: TXT
    value: "v=spf1 -all"
    ttl: 300
only:
  - "*.dev.local"
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}

	if cfg.TTL != 120 {
		t.Errorf("ttl = %d, want 120", cfg.TTL)
	}
	if cfg.Timeout.Duration().String() != "5s" {
		t.Errorf("timeout = %s, want 5s", cfg.Timeout.Duration())
	}
	if cfg.Fallback != FallbackNXDomain {
		t.Errorf("fallback = %s", cfg.Fallback)
	}

	store, err := cfg.BuildStore()
	if err != nil {
		t.Fatal(err)
	}
	// 1 wildcard A + 2 api A + MX + TXT
	if store.Len() != 5 {
		t.Errorf("records = %d, want 5", store.Len())
	}
	if got := store.Lookup("api.dev.local", dns.TypeA); len(got) != 2 {
		t.Errorf("api.dev.local A count = %d, want 2", len(got))
	}
	if cfg.BuildFilter().ShouldFake("example.com") {
		t.Error("only-filter should exclude example.com")
	}
}

func TestLoadRejectsBadInput(t *testing.T) {
	tests := map[string]string{
		"unknown field":    "listen: [127.0.0.1:53]\nnope: 1\n",
		"bad fallback":     "fallback: explode\n",
		"only and except":  "only: [a.com]\nexcept: [b.com]\n",
		"bad record type":  "records:\n  - name: x.local\n    type: NOPE\n    value: y\n",
		"bad address":      "hosts:\n  x.local: not-an-ip\n",
		"bad ipv4 in AAAA": "records:\n  - name: x.local\n    type: AAAA\n    value: 127.0.0.1\n",
		"bad duration":     "timeout: soon\n",
		"empty value":      "records:\n  - name: x.local\n    type: A\n",
	}

	for name, content := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(writeTemp(t, "c.yaml", content)); err == nil {
				t.Error("expected an error")
			}
		})
	}
}

func TestDefaultsAreAppliedToPartialConfig(t *testing.T) {
	cfg, err := Load(writeTemp(t, "c.yaml", "hosts:\n  x.local: 127.0.0.1\n"))
	if err != nil {
		t.Fatal(err)
	}

	d := Default()
	if len(cfg.Listen) != 1 || cfg.Listen[0] != d.Listen[0] {
		t.Errorf("listen = %v, want the default", cfg.Listen)
	}
	if cfg.Fallback != FallbackProxy {
		t.Errorf("fallback = %s, want proxy", cfg.Fallback)
	}
	if cfg.NoDataForKnownNames == nil || !*cfg.NoDataForKnownNames {
		t.Error("nodata_for_known_names should default to true")
	}
}

func TestParseRecordFlag(t *testing.T) {
	tests := []struct {
		in    string
		name  string
		rtype string
		value string
		ttl   uint32
	}{
		{"api.dev.local=10.0.0.1", "api.dev.local", "A", "10.0.0.1", 0},
		{"api.dev.local=::1", "api.dev.local", "AAAA", "::1", 0},
		{"old.dev.local=api.dev.local", "old.dev.local", "CNAME", "api.dev.local", 0},
		{"TXT:dev.local=hello world", "dev.local", "TXT", "hello world", 0},
		{"MX:dev.local=mail.dev.local", "dev.local", "MX", "mail.dev.local", 0},
		{"A:*.dev.local=127.0.0.1#300", "*.dev.local", "A", "127.0.0.1", 300},
		{"*=127.0.0.1", "*", "A", "127.0.0.1", 0},
	}

	for _, tc := range tests {
		got, err := ParseRecordFlag(tc.in)
		if err != nil {
			t.Errorf("ParseRecordFlag(%q): %v", tc.in, err)
			continue
		}
		if got.Name != tc.name || got.Type != tc.rtype || got.TTL != tc.ttl {
			t.Errorf("ParseRecordFlag(%q) = %+v, want %s %s ttl=%d", tc.in, got, tc.rtype, tc.name, tc.ttl)
		}
		if len(got.Value) != 1 || got.Value[0] != tc.value {
			t.Errorf("ParseRecordFlag(%q) value = %v, want %q", tc.in, got.Value, tc.value)
		}
	}

	for _, bad := range []string{"noequals", "=novalue", "NOPE:x.local=1", "A:x.local=1.2.3.4#abc"} {
		if _, err := ParseRecordFlag(bad); err == nil {
			t.Errorf("ParseRecordFlag(%q) should have failed", bad)
		}
	}
}

// TestImportINI runs the importer over a legacy INI file exercising every
// record type the old format supported.
func TestImportINI(t *testing.T) {
	const legacyINI = `[A]     # Queries for IPv4 address records
*.example.com=192.0.2.1

[AAAA]  # Queries for IPv6 address records
*.example.com=2001:db8::1

[MX]    # Queries for mail server records
*.example.com=mail.fake.com

[NS]
*.example.com=ns.fake.com

[CNAME] # Queries for alias records
*.example.com=www.fake.com

[TXT]   # Queries for text records
*.example.com=fake message

[PTR]
*.2.0.192.in-addr.arpa=fake.com

[SOA]
; FORMAT: mname rname t1 t2 t3 t4 t5
*.example.com=ns.fake.com. hostmaster.fake.com. 1 10800 3600 604800 3600

[NAPTR]
; FORMAT: order preference flags service regexp replacement
*.example.com=100 10 U E2U+sip !^.*$!sip:customer-service@fake.com! .

[SRV]
; FORMAT: priority weight port target
*.*.example.com=0 5 5060 sipserver.fake.com
`

	cfg, err := ImportINI(writeTemp(t, "legacy.ini", legacyINI))
	if err != nil {
		t.Fatal(err)
	}

	// A and AAAA land in the hosts shorthand, the rest in records.
	if len(cfg.Hosts) != 1 {
		t.Errorf("hosts = %v, want one entry", cfg.Hosts)
	}
	if got := cfg.Hosts["*.example.com"]; len(got) != 2 {
		t.Errorf("hosts entry = %v, want an A and an AAAA value", got)
	}
	if len(cfg.Records) != 8 {
		t.Errorf("records = %d, want 8", len(cfg.Records))
	}

	cfg.ApplyDefaults()
	store, err := cfg.BuildStore()
	if err != nil {
		t.Fatal(err)
	}
	if store.Len() != 10 {
		t.Errorf("store size = %d, want 10", store.Len())
	}

	// Spot-check that a converted record actually builds an RR.
	srv := store.Lookup("_sip._tcp.example.com", dns.TypeSRV)
	if len(srv) != 1 {
		t.Fatalf("SRV lookup returned %d records", len(srv))
	}
	if _, err := srv[0].RR("_sip._tcp.example.com", 60); err != nil {
		t.Errorf("converted SRV record does not build: %v", err)
	}
}

func TestImportINIReportsUnsupportedSections(t *testing.T) {
	cfg, err := ImportINI(writeTemp(t, "x.ini", "[BOGUS]\nfoo=bar\n\n[A]\nx.local=1.2.3.4\n"))
	if err == nil {
		t.Fatal("expected a SkippedSectionsError")
	}
	var skipped *SkippedSectionsError
	if !strings.Contains(err.Error(), "BOGUS") {
		t.Errorf("error should name the skipped section: %v", err)
	}
	if _, ok := err.(*SkippedSectionsError); !ok {
		t.Errorf("error type = %T, want %T", err, skipped)
	}
	// The rest of the file is still imported.
	if len(cfg.Hosts) != 1 {
		t.Errorf("hosts = %v, want the [A] section to survive", cfg.Hosts)
	}
}

func TestMarshalRoundTrip(t *testing.T) {
	cfg := Default()
	cfg.Hosts = map[string]StringList{"x.local": {"127.0.0.1"}}

	data, err := cfg.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "timeout: 3s") {
		t.Errorf("duration should marshal as a string:\n%s", data)
	}

	reloaded, err := Load(writeTemp(t, "rt.yaml", string(data)))
	if err != nil {
		t.Fatalf("round-tripped config does not load: %v\n%s", err, data)
	}
	if reloaded.Timeout != cfg.Timeout {
		t.Errorf("timeout = %s, want %s", reloaded.Timeout.Duration(), cfg.Timeout.Duration())
	}
}
