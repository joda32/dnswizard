// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Willem Mouton

package cmd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/joda32/dnswizard/internal/config"
	"github.com/joda32/dnswizard/internal/logging"
	"github.com/joda32/dnswizard/internal/records"
	"github.com/joda32/dnswizard/internal/server"
	"github.com/joda32/dnswizard/internal/upstream"
	"github.com/joda32/dnswizard/internal/watcher"
)

// configNames are searched, in order, when --config is not given.
var configNames = []string{"dnswizard.yaml", "dnswizard.yml"}

type serveOptions struct {
	configPath string
	listen     []string
	upstream   []string
	recordArgs []string
	ttl        uint32
	only       []string
	except     []string
	fallback   string
	timeout    time.Duration
	logLevel   string
	logFormat  string
	logFile    string
	watch      bool
	quiet      bool
}

func newServeCommand() *cobra.Command {
	o := &serveOptions{}

	cmd := &cobra.Command{
		Use:     "serve",
		Aliases: []string{"run", "start"},
		Short:   "Run the DNS server",
		Long: strings.TrimSpace(`
Run the DNS server.

With no arguments dnswizard is a plain forwarding proxy that logs every query,
which is a good way to find out what an application is actually looking up.
Add records to start answering some of those queries yourself.`),
		Example: strings.TrimSpace(`
  # Watch what an application resolves, changing nothing
  dnswizard serve

  # Point a whole wildcard domain at your machine, no config file needed
  sudo dnswizard serve -r '*.dev.local=127.0.0.1'

  # Run unprivileged on 5353 with records from a file, reloaded on save
  dnswizard serve -c dnswizard.yaml -l 127.0.0.1:5353

  # Send everything to 127.0.0.1 except the domains you still want real
  sudo dnswizard serve -r '*=127.0.0.1' --except github.com,*.docker.io

  # Intercept only one domain, proxy the rest over DNS-over-TLS
  sudo dnswizard serve -r 'api.example.com=127.0.0.1' --only api.example.com \
      -u tls://1.1.1.1:853`),
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return o.run(cmd) },
	}

	f := cmd.Flags()
	f.StringVarP(&o.configPath, "config", "c", "", "config file (default: ./dnswizard.yaml if present)")
	f.StringSliceVarP(&o.listen, "listen", "l", nil, "address to listen on, repeatable; prefix udp:// or tcp:// to bind one transport (default 127.0.0.1:53)")
	f.StringSliceVarP(&o.upstream, "upstream", "u", nil, "upstream resolver, repeatable; accepts IP, IP:port, tcp://, tls:// (default 1.1.1.1:53, 8.8.8.8:53)")
	f.StringArrayVarP(&o.recordArgs, "record", "r", nil, "record as name=value or TYPE:name=value[#ttl], repeatable")
	f.Uint32Var(&o.ttl, "ttl", 0, "TTL for records that do not set their own (default 60)")
	f.StringSliceVar(&o.only, "only", nil, "fake only these name patterns, proxy the rest")
	f.StringSliceVar(&o.except, "except", nil, "proxy these name patterns, fake the rest")
	f.StringVar(&o.fallback, "fallback", "", "what to do with unmatched queries: proxy, nxdomain, refused, empty (default proxy)")
	f.DurationVar(&o.timeout, "timeout", 0, "upstream query timeout (default 3s)")
	f.StringVar(&o.logLevel, "log-level", "", "debug, info, warn or error (default info)")
	f.StringVar(&o.logFormat, "log-format", "", "console, text or json (default console)")
	f.StringVar(&o.logFile, "log-file", "", "also append logs to this file")
	f.BoolVar(&o.watch, "watch", true, "reload records when the config file changes")
	f.BoolVarP(&o.quiet, "quiet", "q", false, "do not print the banner")

	return cmd
}

func (o *serveOptions) run(cmd *cobra.Command) error {
	cfg, err := o.buildConfig(cmd)
	if err != nil {
		return err
	}

	log, closer, err := logging.Setup(logging.Options{
		Level:  cfg.Log.Level,
		Format: cfg.Log.Format,
		File:   cfg.Log.File,
	})
	if err != nil {
		return err
	}
	defer closer.Close()

	if !o.quiet {
		banner(cmd.OutOrStdout())
	}

	resolver, err := upstream.New(cfg.Upstream, cfg.Timeout.Duration())
	if err != nil {
		return err
	}

	srv, err := server.New(server.Options{Listen: cfg.Listen, Resolver: resolver, Logger: log})
	if err != nil {
		return err
	}
	if err := srv.Reload(cfg); err != nil {
		return err
	}

	store, _ := cfg.BuildStore()
	if cfg.Path != "" {
		log.Info("loaded config", "path", cfg.Path)
	}
	log.Info("ready", "records", store.Len(), "upstream", strings.Join(resolver.Names(), ","),
		"fallback", string(cfg.Fallback), "filter", cfg.BuildFilter().Describe())
	for _, r := range store.All() {
		log.Info("serving", "name", r.Name, "type", records.TypeName(r.Type), "value", r.Value)
	}
	if store.Len() == 0 {
		log.Info("no records configured, running as a logging proxy")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if o.watch && cfg.Path != "" {
		go func() {
			err := watcher.Watch(ctx, cfg.Path, log, func() { o.reload(cmd, srv, cfg, log) })
			if err != nil {
				log.Warn("config watch disabled", "error", err)
			}
		}()
	}

	serveErr := srv.Start(ctx)

	// Only report totals if we actually served; a bind failure should print the
	// error, not a row of zeroes.
	select {
	case <-srv.Ready():
		stats := srv.Stats()
		log.Info("shutting down", "queries", stats.Queries.Load(), "cooked", stats.Cooked.Load(),
			"proxied", stats.Proxied.Load(), "blocked", stats.Blocked.Load(), "failures", stats.Failures.Load())
	default:
	}
	return serveErr
}

// reload re-reads the config file and swaps the record set in place. Listener
// and upstream changes need a restart; say so rather than pretending.
func (o *serveOptions) reload(cmd *cobra.Command, srv *server.Server, old *config.Config, log *slog.Logger) {
	fresh, err := o.buildConfig(cmd)
	if err != nil {
		log.Error("config reload failed, keeping previous records", "error", err)
		return
	}

	if err := srv.Reload(fresh); err != nil {
		log.Error("config reload failed, keeping previous records", "error", err)
		return
	}

	store, _ := fresh.BuildStore()
	log.Info("config reloaded", "records", store.Len())

	if !equalStrings(old.Listen, fresh.Listen) {
		log.Warn("listen addresses changed; restart dnswizard to apply them")
	}
	if !equalStrings(old.Upstream, fresh.Upstream) {
		log.Warn("upstream servers changed; restart dnswizard to apply them")
	}
	old.Listen, old.Upstream = fresh.Listen, fresh.Upstream
}

// buildConfig resolves the effective configuration: defaults, then the config
// file, then command-line flags on top.
func (o *serveOptions) buildConfig(cmd *cobra.Command) (*config.Config, error) {
	path, err := o.findConfig()
	if err != nil {
		return nil, err
	}

	cfg := config.Default()
	if path != "" {
		cfg, err = config.Load(path)
		if err != nil {
			return nil, err
		}
	}

	f := cmd.Flags()
	if f.Changed("listen") {
		cfg.Listen = o.listen
	}
	if f.Changed("upstream") {
		cfg.Upstream = o.upstream
	}
	if f.Changed("ttl") {
		cfg.TTL = o.ttl
	}
	if f.Changed("timeout") {
		cfg.Timeout = config.Duration(o.timeout)
	}
	if f.Changed("fallback") {
		cfg.Fallback = config.Fallback(strings.ToLower(o.fallback))
	}
	if f.Changed("only") {
		cfg.Only, cfg.Except = o.only, nil
	}
	if f.Changed("except") {
		cfg.Except, cfg.Only = o.except, nil
	}
	if f.Changed("log-level") {
		cfg.Log.Level = o.logLevel
	}
	if f.Changed("log-format") {
		cfg.Log.Format = o.logFormat
	}
	if f.Changed("log-file") {
		cfg.Log.File = o.logFile
	}

	// Command-line records are appended after file records. Both are matched by
	// specificity, so an exact --record still wins over a wildcard in the file.
	for _, arg := range o.recordArgs {
		spec, err := config.ParseRecordFlag(arg)
		if err != nil {
			return nil, fmt.Errorf("--record %w", err)
		}
		cfg.Records = append(cfg.Records, spec)
	}

	cfg.ApplyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// findConfig returns the config file to use, or "" to run on flags alone.
func (o *serveOptions) findConfig() (string, error) {
	if o.configPath != "" {
		if _, err := os.Stat(o.configPath); err != nil {
			return "", fmt.Errorf("config file: %w", err)
		}
		return o.configPath, nil
	}

	for _, name := range configNames {
		if _, err := os.Stat(name); err == nil {
			abs, err := filepath.Abs(name)
			if err != nil {
				return name, nil //nolint:nilerr // a relative path still works
			}
			return abs, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
	}
	return "", nil
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
