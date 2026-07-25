// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Willem Mouton

// Package server implements the DNS listeners and the query handler.
package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/miekg/dns"

	"github.com/joda32/dnswizard/internal/config"
	"github.com/joda32/dnswizard/internal/records"
	"github.com/joda32/dnswizard/internal/upstream"
)

// maxCNAMEDepth bounds local CNAME chasing so a config with a loop in it
// cannot spin the handler.
const maxCNAMEDepth = 8

// Options configures a Server. Listen and Resolver are fixed for the lifetime
// of the process; the store, filter and behaviour flags can be swapped at
// runtime by Reload.
type Options struct {
	Listen   []string
	Resolver *upstream.Resolver
	Logger   *slog.Logger
}

// runtime holds everything that a config reload can replace, so the whole set
// swaps atomically and a query never sees a mix of old and new.
type runtime struct {
	store               *records.Store
	filter              *records.Filter
	fallback            config.Fallback
	noDataForKnownNames bool
	chaseCNAME          bool
}

// Server answers DNS queries from a record store, forwarding whatever it does
// not have an answer for.
type Server struct {
	log      *slog.Logger
	resolver *upstream.Resolver
	listen   []string

	current atomic.Pointer[runtime]

	ready     chan struct{} // closed once every listener is bound
	mu        sync.Mutex
	listeners []*dns.Server

	stats Stats
}

// Ready is closed once every listener is bound, which is the point at which
// the server can accept queries.
func (s *Server) Ready() <-chan struct{} { return s.ready }

// Addrs reports the addresses actually bound, which is how you find the port
// when you asked for :0.
func (s *Server) Addrs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]string, 0, len(s.listeners))
	for _, srv := range s.listeners {
		switch {
		case srv.PacketConn != nil:
			out = append(out, "udp://"+srv.PacketConn.LocalAddr().String())
		case srv.Listener != nil:
			out = append(out, "tcp://"+srv.Listener.Addr().String())
		}
	}
	return out
}

// Stats counts what the server has done since it started.
type Stats struct {
	Queries  atomic.Uint64
	Cooked   atomic.Uint64
	Proxied  atomic.Uint64
	Blocked  atomic.Uint64
	Failures atomic.Uint64
}

// New creates a server. Call Reload before Start to install the initial
// record set.
func New(opts Options) (*Server, error) {
	if opts.Resolver == nil {
		return nil, errors.New("server: no resolver")
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if len(opts.Listen) == 0 {
		return nil, errors.New("server: no listen addresses")
	}
	return &Server{
		log:      opts.Logger,
		resolver: opts.Resolver,
		listen:   opts.Listen,
		ready:    make(chan struct{}),
	}, nil
}

// Reload swaps in a new record set and behaviour. Safe to call while serving.
func (s *Server) Reload(cfg *config.Config) error {
	store, err := cfg.BuildStore()
	if err != nil {
		return err
	}
	s.current.Store(&runtime{
		store:               store,
		filter:              cfg.BuildFilter(),
		fallback:            cfg.Fallback,
		noDataForKnownNames: cfg.NoDataForKnownNames == nil || *cfg.NoDataForKnownNames,
		chaseCNAME:          cfg.ChaseCNAME == nil || *cfg.ChaseCNAME,
	})
	return nil
}

// Stats exposes the running counters.
func (s *Server) Stats() *Stats { return &s.stats }

// listenSpec is one parsed listen address.
type listenSpec struct {
	net  string // "udp" or "tcp"
	addr string // host:port
}

// parseListen expands a listen address into concrete transports. A bare
// address binds both UDP and TCP; "udp://" or "tcp://" pins one.
func parseListen(spec string) ([]listenSpec, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil, errors.New("empty listen address")
	}

	nets := []string{"udp", "tcp"}
	if scheme, rest, ok := strings.Cut(spec, "://"); ok {
		switch strings.ToLower(scheme) {
		case "udp":
			nets = []string{"udp"}
		case "tcp":
			nets = []string{"tcp"}
		default:
			return nil, fmt.Errorf("unknown listen scheme %q", scheme)
		}
		spec = rest
	}

	if _, _, err := net.SplitHostPort(spec); err != nil {
		if ip := net.ParseIP(spec); ip != nil {
			spec = net.JoinHostPort(ip.String(), "53")
		} else {
			return nil, fmt.Errorf("cannot parse listen address %q: %w", spec, err)
		}
	}

	out := make([]listenSpec, 0, len(nets))
	for _, n := range nets {
		out = append(out, listenSpec{net: n, addr: spec})
	}
	return out, nil
}

// Start binds every listener and serves until ctx is cancelled.
//
// Sockets are opened before any goroutine starts serving, so a bind failure
// (the classic "port 53 needs root") is reported synchronously rather than
// swallowed by a background goroutine.
func (s *Server) Start(ctx context.Context) error {
	if s.current.Load() == nil {
		return errors.New("server: Reload must be called before Start")
	}

	var specs []listenSpec
	for _, l := range s.listen {
		parsed, err := parseListen(l)
		if err != nil {
			return err
		}
		specs = append(specs, parsed...)
	}

	var started []*dns.Server
	cleanup := func() {
		for _, srv := range started {
			_ = srv.Shutdown()
		}
	}

	for _, spec := range specs {
		srv := &dns.Server{Addr: spec.addr, Net: spec.net, Handler: s}

		switch spec.net {
		case "udp":
			pc, err := net.ListenPacket("udp", spec.addr)
			if err != nil {
				cleanup()
				return bindError(spec, err)
			}
			srv.PacketConn = pc
		case "tcp":
			ln, err := net.Listen("tcp", spec.addr)
			if err != nil {
				cleanup()
				return bindError(spec, err)
			}
			srv.Listener = ln
		}

		started = append(started, srv)
		s.log.Info("listening", "proto", spec.net, "addr", spec.addr)
	}

	s.mu.Lock()
	s.listeners = started
	s.mu.Unlock()
	close(s.ready)

	errCh := make(chan error, len(started))
	var wg sync.WaitGroup
	for _, srv := range started {
		wg.Add(1)
		go func(srv *dns.Server) {
			defer wg.Done()
			if err := srv.ActivateAndServe(); err != nil {
				errCh <- fmt.Errorf("%s %s: %w", srv.Net, srv.Addr, err)
			}
		}(srv)
	}

	var serveErr error
	select {
	case <-ctx.Done():
	case serveErr = <-errCh:
	}

	cleanup()
	wg.Wait()
	return serveErr
}

func bindError(spec listenSpec, err error) error {
	msg := fmt.Errorf("cannot bind %s %s: %w", spec.net, spec.addr, err)
	if errors.Is(err, os.ErrPermission) {
		return fmt.Errorf("%w (ports below 1024 need root: try sudo, or --listen 127.0.0.1:5353)", msg)
	}
	return msg
}
