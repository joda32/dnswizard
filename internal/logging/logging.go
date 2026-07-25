// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Willem Mouton

// Package logging configures the slog logger used across dnswizard.
//
// Three output formats are available: a compact colourised console format
// meant for watching queries scroll by in a terminal, plus the standard slog
// text and JSON handlers for when the output is being piped somewhere.
package logging

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
)

// Options controls logger construction.
type Options struct {
	Level   string // debug, info, warn, error
	Format  string // console, text, json
	File    string // optional path to also write logs to
	NoColor bool
}

// Setup builds a logger from opts. The returned io.Closer closes the log file
// (if one was opened) and is safe to call even when no file was configured.
func Setup(opts Options) (*slog.Logger, io.Closer, error) {
	level, err := ParseLevel(opts.Level)
	if err != nil {
		return nil, nil, err
	}

	var (
		out    io.Writer = os.Stderr
		closer io.Closer = noopCloser{}
	)

	if opts.File != "" {
		f, err := os.OpenFile(opts.File, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return nil, nil, fmt.Errorf("open log file: %w", err)
		}
		// The file always gets the plain (uncoloured) console format so it stays
		// readable, while the terminal keeps whatever format was requested.
		out = io.MultiWriter(os.Stderr, f)
		closer = f
	}

	var handler slog.Handler
	switch strings.ToLower(opts.Format) {
	case "json":
		handler = slog.NewJSONHandler(out, &slog.HandlerOptions{Level: level})
	case "text":
		handler = slog.NewTextHandler(out, &slog.HandlerOptions{Level: level})
	case "", "console":
		color := !opts.NoColor && opts.File == "" && isTerminal(os.Stderr)
		handler = newConsoleHandler(out, level, color)
	default:
		return nil, nil, fmt.Errorf("unknown log format %q (want console, text or json)", opts.Format)
	}

	return slog.New(handler), closer, nil
}

// ParseLevel maps a level name onto a slog.Level.
func ParseLevel(name string) (slog.Level, error) {
	switch strings.ToLower(name) {
	case "", "info":
		return slog.LevelInfo, nil
	case "debug":
		return slog.LevelDebug, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("unknown log level %q (want debug, info, warn or error)", name)
	}
}

type noopCloser struct{}

func (noopCloser) Close() error { return nil }

func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// consoleHandler renders records as "15:04:05 INF  message  key=value".
type consoleHandler struct {
	mu     *sync.Mutex
	out    io.Writer
	level  slog.Level
	color  bool
	groups []string
	attrs  []slog.Attr
}

func newConsoleHandler(out io.Writer, level slog.Level, color bool) *consoleHandler {
	return &consoleHandler{mu: &sync.Mutex{}, out: out, level: level, color: color}
}

func (h *consoleHandler) Enabled(_ context.Context, l slog.Level) bool { return l >= h.level }

func (h *consoleHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	clone := *h
	clone.attrs = append(append([]slog.Attr{}, h.attrs...), attrs...)
	return &clone
}

func (h *consoleHandler) WithGroup(name string) slog.Handler {
	clone := *h
	clone.groups = append(append([]string{}, h.groups...), name)
	return &clone
}

const (
	ansiReset = "\033[0m"
	ansiDim   = "\033[2m"
	ansiCyan  = "\033[36m"
)

func (h *consoleHandler) Handle(_ context.Context, r slog.Record) error {
	var b strings.Builder

	if h.color {
		b.WriteString(ansiDim)
	}
	b.WriteString(r.Time.Format("15:04:05"))
	if h.color {
		b.WriteString(ansiReset)
	}

	tag, color := levelTag(r.Level)
	b.WriteByte(' ')
	if h.color {
		b.WriteString(color)
	}
	b.WriteString(tag)
	if h.color {
		b.WriteString(ansiReset)
	}

	b.WriteByte(' ')
	b.WriteString(r.Message)

	write := func(a slog.Attr) bool {
		a.Value = a.Value.Resolve()
		if a.Equal(slog.Attr{}) {
			return true
		}
		b.WriteByte(' ')
		if h.color {
			b.WriteString(ansiCyan)
		}
		if len(h.groups) > 0 {
			b.WriteString(strings.Join(h.groups, "."))
			b.WriteByte('.')
		}
		b.WriteString(a.Key)
		b.WriteByte('=')
		if h.color {
			b.WriteString(ansiReset)
		}
		b.WriteString(quoteIfNeeded(a.Value.String()))
		return true
	}

	for _, a := range h.attrs {
		write(a)
	}
	r.Attrs(write)
	b.WriteByte('\n')

	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := io.WriteString(h.out, b.String())
	return err
}

func levelTag(l slog.Level) (tag, color string) {
	switch {
	case l >= slog.LevelError:
		return "ERR ", "\033[31m"
	case l >= slog.LevelWarn:
		return "WRN ", "\033[33m"
	case l >= slog.LevelInfo:
		return "INF ", "\033[32m"
	default:
		return "DBG ", "\033[35m"
	}
}

func quoteIfNeeded(s string) string {
	if s == "" {
		return `""`
	}
	if strings.ContainsAny(s, " \t\"") {
		return fmt.Sprintf("%q", s)
	}
	return s
}
