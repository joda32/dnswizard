// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Willem Mouton

package server

import (
	"context"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/miekg/dns"

	"github.com/joda32/dnswizard/internal/config"
	"github.com/joda32/dnswizard/internal/upstream"
)

// stubUpstream is a DNS server that answers everything with a fixed address,
// standing in for the real internet.
type stubUpstream struct {
	addr    string
	queries atomic.Uint64
	answer  string
	rcode   int
}

func newStubUpstream(t *testing.T, answer string) *stubUpstream {
	t.Helper()

	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	stub := &stubUpstream{addr: pc.LocalAddr().String(), answer: answer, rcode: dns.RcodeSuccess}
	srv := &dns.Server{PacketConn: pc, Handler: dns.HandlerFunc(func(w dns.ResponseWriter, r *dns.Msg) {
		stub.queries.Add(1)

		m := new(dns.Msg)
		m.SetRcode(r, stub.rcode)
		m.RecursionAvailable = true
		if stub.rcode == dns.RcodeSuccess && len(r.Question) == 1 && r.Question[0].Qtype == dns.TypeA {
			rr, err := dns.NewRR(r.Question[0].Name + " 60 IN A " + stub.answer)
			if err == nil {
				m.Answer = append(m.Answer, rr)
			}
		}
		_ = w.WriteMsg(m)
	})}

	go func() { _ = srv.ActivateAndServe() }()
	t.Cleanup(func() { _ = srv.Shutdown() })
	return stub
}

// startServer boots a dnswizard on an ephemeral UDP port and returns its
// address plus the stub upstream behind it.
func startServer(t *testing.T, cfg *config.Config) (string, *stubUpstream, *Server) {
	t.Helper()

	stub := newStubUpstream(t, "9.9.9.9")

	cfg.Listen = []string{"udp://127.0.0.1:0"}
	cfg.Upstream = []string{stub.addr}
	cfg.ApplyDefaults()
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}

	resolver, err := upstream.New(cfg.Upstream, cfg.Timeout.Duration())
	if err != nil {
		t.Fatal(err)
	}

	srv, err := New(Options{
		Listen:   cfg.Listen,
		Resolver: resolver,
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Reload(cfg); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("server did not shut down")
		}
	})

	select {
	case <-srv.Ready():
	case <-time.After(2 * time.Second):
		t.Fatal("server did not become ready")
	}

	addrs := srv.Addrs()
	if len(addrs) != 1 {
		t.Fatalf("got %d listeners, want 1", len(addrs))
	}
	return strings.TrimPrefix(addrs[0], "udp://"), stub, srv
}

func query(t *testing.T, addr, name string, qtype uint16) *dns.Msg {
	t.Helper()

	m := new(dns.Msg)
	m.SetQuestion(dns.Fqdn(name), qtype)
	m.RecursionDesired = true

	c := &dns.Client{Timeout: 2 * time.Second}
	reply, _, err := c.Exchange(m, addr)
	if err != nil {
		t.Fatalf("query %s %s: %v", name, dns.TypeToString[qtype], err)
	}
	return reply
}

func firstA(t *testing.T, m *dns.Msg) string {
	t.Helper()
	for _, rr := range m.Answer {
		if a, ok := rr.(*dns.A); ok {
			return a.A.String()
		}
	}
	t.Fatalf("no A record in answer: %v", m.Answer)
	return ""
}

func TestCookedAndProxiedAnswers(t *testing.T) {
	cfg := &config.Config{
		Hosts: map[string]config.StringList{"*.dev.local": {"127.0.0.1"}},
	}
	addr, stub, _ := startServer(t, cfg)

	// A matching name is answered locally, without touching the upstream.
	reply := query(t, addr, "api.dev.local", dns.TypeA)
	if got := firstA(t, reply); got != "127.0.0.1" {
		t.Errorf("api.dev.local = %s, want 127.0.0.1", got)
	}
	if !reply.Authoritative {
		t.Error("a cooked answer should be authoritative")
	}
	if n := stub.queries.Load(); n != 0 {
		t.Errorf("upstream was consulted %d times for a local name", n)
	}

	// Anything else is forwarded.
	reply = query(t, addr, "example.com", dns.TypeA)
	if got := firstA(t, reply); got != "9.9.9.9" {
		t.Errorf("example.com = %s, want the upstream answer", got)
	}
	if n := stub.queries.Load(); n != 1 {
		t.Errorf("upstream query count = %d, want 1", n)
	}
}

func TestKnownNameWithOtherTypeReturnsNodata(t *testing.T) {
	cfg := &config.Config{
		Hosts: map[string]config.StringList{"api.dev.local": {"10.0.0.1"}},
	}
	addr, stub, _ := startServer(t, cfg)

	reply := query(t, addr, "api.dev.local", dns.TypeMX)
	if reply.Rcode != dns.RcodeSuccess {
		t.Errorf("rcode = %s, want NOERROR", dns.RcodeToString[reply.Rcode])
	}
	if len(reply.Answer) != 0 {
		t.Errorf("want an empty answer, got %v", reply.Answer)
	}
	if n := stub.queries.Load(); n != 0 {
		t.Error("a locally known name should not leak to the upstream")
	}
}

func TestKnownNameLeaksWhenNodataDisabled(t *testing.T) {
	off := false
	cfg := &config.Config{
		Hosts:               map[string]config.StringList{"api.dev.local": {"10.0.0.1"}},
		NoDataForKnownNames: &off,
	}
	addr, stub, _ := startServer(t, cfg)

	query(t, addr, "api.dev.local", dns.TypeMX)
	if n := stub.queries.Load(); n != 1 {
		t.Errorf("with nodata_for_known_names off the query should be proxied, got %d upstream queries", n)
	}
}

func TestANYReturnsEveryKnownRecord(t *testing.T) {
	cfg := &config.Config{
		Hosts: map[string]config.StringList{"dev.local": {"10.0.0.1"}},
		Records: []config.RecordSpec{
			{Name: "dev.local", Type: "TXT", Value: config.StringList{"hello"}},
			{Name: "dev.local", Type: "MX", Value: config.StringList{"mail.dev.local"}},
		},
	}
	addr, _, _ := startServer(t, cfg)

	reply := query(t, addr, "dev.local", dns.TypeANY)
	if len(reply.Answer) != 3 {
		t.Fatalf("got %d answers, want 3: %v", len(reply.Answer), reply.Answer)
	}

	seen := map[uint16]bool{}
	for _, rr := range reply.Answer {
		seen[rr.Header().Rrtype] = true
	}
	for _, want := range []uint16{dns.TypeA, dns.TypeTXT, dns.TypeMX} {
		if !seen[want] {
			t.Errorf("ANY answer is missing a %s record", dns.TypeToString[want])
		}
	}
}

func TestFallbackModes(t *testing.T) {
	tests := []struct {
		fallback config.Fallback
		rcode    int
	}{
		{config.FallbackNXDomain, dns.RcodeNameError},
		{config.FallbackRefused, dns.RcodeRefused},
		{config.FallbackEmpty, dns.RcodeSuccess},
	}

	for _, tc := range tests {
		t.Run(string(tc.fallback), func(t *testing.T) {
			addr, stub, _ := startServer(t, &config.Config{Fallback: tc.fallback})

			reply := query(t, addr, "example.com", dns.TypeA)
			if reply.Rcode != tc.rcode {
				t.Errorf("rcode = %s, want %s", dns.RcodeToString[reply.Rcode], dns.RcodeToString[tc.rcode])
			}
			if len(reply.Answer) != 0 {
				t.Errorf("want no answers, got %v", reply.Answer)
			}
			if n := stub.queries.Load(); n != 0 {
				t.Error("a non-proxy fallback should not contact the upstream")
			}
		})
	}
}

func TestOnlyAndExceptFilters(t *testing.T) {
	t.Run("only", func(t *testing.T) {
		cfg := &config.Config{
			Hosts: map[string]config.StringList{"*": {"127.0.0.1"}},
			Only:  []string{"*.dev.local"},
		}
		addr, _, _ := startServer(t, cfg)

		if got := firstA(t, query(t, addr, "api.dev.local", dns.TypeA)); got != "127.0.0.1" {
			t.Errorf("listed name = %s, want the fake answer", got)
		}
		if got := firstA(t, query(t, addr, "example.com", dns.TypeA)); got != "9.9.9.9" {
			t.Errorf("unlisted name = %s, want the real answer", got)
		}
	})

	t.Run("except", func(t *testing.T) {
		cfg := &config.Config{
			Hosts:  map[string]config.StringList{"*": {"127.0.0.1"}},
			Except: []string{"github.com", "*.github.com"},
		}
		addr, _, _ := startServer(t, cfg)

		if got := firstA(t, query(t, addr, "api.github.com", dns.TypeA)); got != "9.9.9.9" {
			t.Errorf("excluded name = %s, want the real answer", got)
		}
		if got := firstA(t, query(t, addr, "example.com", dns.TypeA)); got != "127.0.0.1" {
			t.Errorf("other name = %s, want the fake answer", got)
		}
	})
}

func TestCNAMEChasedLocally(t *testing.T) {
	cfg := &config.Config{
		Hosts: map[string]config.StringList{"api.dev.local": {"10.0.0.5"}},
		Records: []config.RecordSpec{
			{Name: "old.dev.local", Type: "CNAME", Value: config.StringList{"api.dev.local"}},
		},
	}
	addr, stub, _ := startServer(t, cfg)

	reply := query(t, addr, "old.dev.local", dns.TypeA)
	if len(reply.Answer) != 2 {
		t.Fatalf("got %d answers, want CNAME + A: %v", len(reply.Answer), reply.Answer)
	}
	if _, ok := reply.Answer[0].(*dns.CNAME); !ok {
		t.Errorf("first answer should be the CNAME, got %T", reply.Answer[0])
	}
	if got := firstA(t, reply); got != "10.0.0.5" {
		t.Errorf("chased address = %s, want 10.0.0.5", got)
	}
	if n := stub.queries.Load(); n != 0 {
		t.Error("a fully local chain should not need the upstream")
	}
}

// A wildcard A record covering the whole zone must not shadow an explicit
// CNAME sitting at one name inside it.
func TestExplicitCNAMEBeatsWildcardAddress(t *testing.T) {
	cfg := &config.Config{
		Hosts: map[string]config.StringList{
			"*.dev.local":   {"127.0.0.1"},
			"api.dev.local": {"10.0.0.5"},
		},
		Records: []config.RecordSpec{
			{Name: "old.dev.local", Type: "CNAME", Value: config.StringList{"api.dev.local"}},
		},
	}
	addr, _, _ := startServer(t, cfg)

	reply := query(t, addr, "old.dev.local", dns.TypeA)
	if len(reply.Answer) != 2 {
		t.Fatalf("got %d answers, want CNAME + A: %v", len(reply.Answer), reply.Answer)
	}
	if _, ok := reply.Answer[0].(*dns.CNAME); !ok {
		t.Errorf("first answer should be the CNAME, got %T", reply.Answer[0])
	}
	if got := firstA(t, reply); got != "10.0.0.5" {
		t.Errorf("address = %s, want the CNAME target's address, not the wildcard's", got)
	}
}

func TestCNAMEToExternalNameIsResolvedUpstream(t *testing.T) {
	cfg := &config.Config{
		Records: []config.RecordSpec{
			{Name: "alias.dev.local", Type: "CNAME", Value: config.StringList{"example.com"}},
		},
	}
	addr, stub, _ := startServer(t, cfg)

	reply := query(t, addr, "alias.dev.local", dns.TypeA)
	if got := firstA(t, reply); got != "9.9.9.9" {
		t.Errorf("chased address = %s, want the upstream answer", got)
	}
	if n := stub.queries.Load(); n != 1 {
		t.Errorf("upstream queries = %d, want 1", n)
	}
}

func TestUpstreamFailureReturnsServfail(t *testing.T) {
	cfg := &config.Config{}
	cfg.ApplyDefaults()
	cfg.Listen = []string{"udp://127.0.0.1:0"}
	// A port nothing is listening on, so every exchange times out.
	cfg.Upstream = []string{"127.0.0.1:1"}
	cfg.Timeout = config.Duration(200 * time.Millisecond)

	resolver, err := upstream.New(cfg.Upstream, cfg.Timeout.Duration())
	if err != nil {
		t.Fatal(err)
	}
	srv, err := New(Options{Listen: cfg.Listen, Resolver: resolver,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Reload(cfg); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = srv.Start(ctx) }()
	<-srv.Ready()

	addr := strings.TrimPrefix(srv.Addrs()[0], "udp://")
	if reply := query(t, addr, "example.com", dns.TypeA); reply.Rcode != dns.RcodeServerFailure {
		t.Errorf("rcode = %s, want SERVFAIL", dns.RcodeToString[reply.Rcode])
	}
}

func TestReloadSwapsRecordsWhileServing(t *testing.T) {
	cfg := &config.Config{Hosts: map[string]config.StringList{"api.dev.local": {"10.0.0.1"}}}
	addr, _, srv := startServer(t, cfg)

	if got := firstA(t, query(t, addr, "api.dev.local", dns.TypeA)); got != "10.0.0.1" {
		t.Fatalf("before reload = %s", got)
	}

	updated := &config.Config{Hosts: map[string]config.StringList{"api.dev.local": {"10.0.0.2"}}}
	updated.ApplyDefaults()
	if err := srv.Reload(updated); err != nil {
		t.Fatal(err)
	}

	if got := firstA(t, query(t, addr, "api.dev.local", dns.TypeA)); got != "10.0.0.2" {
		t.Errorf("after reload = %s, want 10.0.0.2", got)
	}
}

func TestMultipleAddressesAreAllReturned(t *testing.T) {
	cfg := &config.Config{
		Hosts: map[string]config.StringList{"api.dev.local": {"10.0.0.1", "10.0.0.2"}},
	}
	addr, _, _ := startServer(t, cfg)

	reply := query(t, addr, "api.dev.local", dns.TypeA)
	if len(reply.Answer) != 2 {
		t.Errorf("got %d answers, want both addresses: %v", len(reply.Answer), reply.Answer)
	}
}

func TestQueryIDAndQuestionArePreserved(t *testing.T) {
	addr, _, _ := startServer(t, &config.Config{})

	m := new(dns.Msg)
	m.SetQuestion("example.com.", dns.TypeA)
	m.Id = 4242

	c := &dns.Client{Timeout: 2 * time.Second}
	reply, _, err := c.Exchange(m, addr)
	if err != nil {
		t.Fatal(err)
	}
	if reply.Id != 4242 {
		t.Errorf("id = %d, want the request id echoed back", reply.Id)
	}
	if len(reply.Question) != 1 || reply.Question[0].Name != "example.com." {
		t.Errorf("question section was not echoed: %v", reply.Question)
	}
}
