// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Willem Mouton

// Package upstream forwards queries that dnswizard does not answer itself.
package upstream

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync/atomic"
	"time"

	"github.com/miekg/dns"
)

// Server is a single upstream resolver.
type Server struct {
	Addr    string // host:port
	Net     string // "udp", "tcp" or "tcp-tls"
	TLSName string // SNI / certificate name, DoT only
}

// String renders the server the way it would be written on the command line.
func (s Server) String() string {
	switch s.Net {
	case "tcp":
		return "tcp://" + s.Addr
	case "tcp-tls":
		if s.TLSName != "" {
			return "tls://" + s.Addr + "#" + s.TLSName
		}
		return "tls://" + s.Addr
	default:
		return s.Addr
	}
}

// ParseServer accepts the several ways an upstream can be written:
//
//	1.1.1.1                     plain UDP on port 53
//	1.1.1.1:5353                explicit port
//	2001:4860:4860::8888        bare IPv6, port 53
//	[2001:4860:4860::8888]:53   IPv6 with port
//	tcp://9.9.9.9:53            force TCP
//	tls://1.1.1.1:853#cloudflare-dns.com   DNS over TLS, with certificate name
//	8.8.8.8#53#tcp              legacy HOST#PORT#PROTOCOL syntax
func ParseServer(spec string) (Server, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return Server{}, errors.New("empty upstream")
	}

	srv := Server{Net: "udp"}
	defaultPort := "53"

	if scheme, rest, ok := strings.Cut(spec, "://"); ok {
		switch strings.ToLower(scheme) {
		case "udp", "dns":
			srv.Net = "udp"
		case "tcp":
			srv.Net = "tcp"
		case "tls", "dot":
			srv.Net, defaultPort = "tcp-tls", "853"
		default:
			return Server{}, fmt.Errorf("unknown upstream scheme %q", scheme)
		}
		spec = rest
		// For DoT the part after '#' names the certificate to expect.
		if host, name, ok := strings.Cut(spec, "#"); ok {
			spec, srv.TLSName = host, name
		}
	} else if strings.Contains(spec, "#") {
		// Legacy syntax: HOST#PORT[#PROTOCOL]
		parts := strings.Split(spec, "#")
		if len(parts) > 3 {
			return Server{}, fmt.Errorf("cannot parse upstream %q", spec)
		}
		spec = parts[0]
		if len(parts) > 1 && parts[1] != "" {
			defaultPort = parts[1]
			spec = net.JoinHostPort(parts[0], parts[1])
		}
		if len(parts) > 2 {
			switch strings.ToLower(parts[2]) {
			case "udp":
				srv.Net = "udp"
			case "tcp":
				srv.Net = "tcp"
			default:
				return Server{}, fmt.Errorf("unknown upstream protocol %q", parts[2])
			}
		}
	}

	if _, _, err := net.SplitHostPort(spec); err == nil {
		srv.Addr = spec
	} else if ip := net.ParseIP(spec); ip != nil {
		srv.Addr = net.JoinHostPort(ip.String(), defaultPort)
	} else if spec != "" && !strings.ContainsAny(spec, " \t") {
		srv.Addr = net.JoinHostPort(spec, defaultPort)
	} else {
		return Server{}, fmt.Errorf("cannot parse upstream %q", spec)
	}

	if srv.Net == "tcp-tls" && srv.TLSName == "" {
		if host, _, err := net.SplitHostPort(srv.Addr); err == nil && net.ParseIP(host) == nil {
			srv.TLSName = host
		}
	}
	return srv, nil
}

// Resolver forwards queries to a list of upstream servers.
//
// Servers are tried in order starting from a rotating offset, so load is spread
// across them but a dead server only costs one timeout before the next is
// tried, rather than picking one at random with no failover.
type Resolver struct {
	servers []Server
	timeout time.Duration
	udp     *dns.Client
	tcp     *dns.Client
	tls     *dns.Client
	cursor  atomic.Uint64
}

// New builds a resolver from upstream specifications.
func New(specs []string, timeout time.Duration) (*Resolver, error) {
	if timeout <= 0 {
		timeout = 3 * time.Second
	}

	r := &Resolver{
		timeout: timeout,
		udp:     &dns.Client{Net: "udp", Timeout: timeout, UDPSize: dns.DefaultMsgSize},
		tcp:     &dns.Client{Net: "tcp", Timeout: timeout},
		tls:     &dns.Client{Net: "tcp-tls", Timeout: timeout, TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12}},
	}

	for _, spec := range specs {
		if strings.TrimSpace(spec) == "" {
			continue
		}
		srv, err := ParseServer(spec)
		if err != nil {
			return nil, err
		}
		r.servers = append(r.servers, srv)
	}
	if len(r.servers) == 0 {
		return nil, errors.New("no upstream servers configured")
	}
	return r, nil
}

// Servers returns the configured upstreams in order.
func (r *Resolver) Servers() []Server { return r.servers }

// Names renders the upstreams for logging.
func (r *Resolver) Names() []string {
	out := make([]string, len(r.servers))
	for i, s := range r.servers {
		out[i] = s.String()
	}
	return out
}

// Result carries an upstream reply along with which server produced it.
type Result struct {
	Msg    *dns.Msg
	Server Server
	RTT    time.Duration
}

// Exchange forwards m to the upstreams, returning the first successful reply.
//
// A truncated UDP answer is retried over TCP against the same server, so large
// responses survive even though the client asked over UDP.
func (r *Resolver) Exchange(ctx context.Context, m *dns.Msg) (*Result, error) {
	start := int(r.cursor.Add(1) - 1)
	var errs []error

	for i := range r.servers {
		srv := r.servers[(start+i)%len(r.servers)]

		reply, rtt, err := r.exchangeWith(ctx, srv, m)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", srv, err))
			continue
		}

		if reply.Truncated && srv.Net == "udp" {
			tcpSrv := srv
			tcpSrv.Net = "tcp"
			if retry, rtt2, err := r.exchangeWith(ctx, tcpSrv, m); err == nil {
				return &Result{Msg: retry, Server: tcpSrv, RTT: rtt + rtt2}, nil
			}
		}
		return &Result{Msg: reply, Server: srv, RTT: rtt}, nil
	}

	return nil, fmt.Errorf("all upstreams failed: %w", errors.Join(errs...))
}

func (r *Resolver) exchangeWith(ctx context.Context, srv Server, m *dns.Msg) (*dns.Msg, time.Duration, error) {
	client := r.udp
	switch srv.Net {
	case "tcp":
		client = r.tcp
	case "tcp-tls":
		c := *r.tls
		cfg := c.TLSConfig.Clone()
		cfg.ServerName = srv.TLSName
		c.TLSConfig = cfg
		client = &c
	}

	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	reply, rtt, err := client.ExchangeContext(ctx, m, srv.Addr)
	if err != nil {
		return nil, rtt, err
	}
	if reply == nil {
		return nil, rtt, errors.New("empty reply")
	}
	return reply, rtt, nil
}
