// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Willem Mouton

// Package watcher reloads the config file when it changes on disk.
package watcher

import (
	"context"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
)

// debounce collapses the burst of events an editor produces when saving.
const debounce = 250 * time.Millisecond

// Watch calls onChange whenever path is written, created or replaced, until
// ctx is cancelled.
//
// The watch is on the containing directory rather than the file itself:
// editors and `mv` replace a file by renaming a new one over it, which would
// silently detach a watch registered against the original inode.
func Watch(ctx context.Context, path string, log *slog.Logger, onChange func()) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}

	w, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer w.Close()

	if err := w.Add(filepath.Dir(abs)); err != nil {
		return err
	}
	log.Debug("watching config for changes", "path", abs)

	var timer *time.Timer
	var fired <-chan time.Time

	for {
		select {
		case <-ctx.Done():
			return nil

		case event, ok := <-w.Events:
			if !ok {
				return nil
			}
			eventPath, err := filepath.Abs(event.Name)
			if err != nil || eventPath != abs {
				continue
			}
			if event.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename) == 0 {
				continue
			}
			if timer != nil {
				timer.Stop()
			}
			timer = time.NewTimer(debounce)
			fired = timer.C

		case <-fired:
			fired = nil
			onChange()

		case err, ok := <-w.Errors:
			if !ok {
				return nil
			}
			log.Warn("config watcher error", "error", err)
		}
	}
}
