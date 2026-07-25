// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Willem Mouton

// Package cmd implements the dnswizard command-line interface.
package cmd

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

// Version is overridden at build time with -ldflags "-X .../cmd.Version=...".
var Version = "dev"

const bannerArt = `     _                        _                        _
  __| |_ __  _____      _____(_)______ _ _ __ __ _  __| |
 / _` + "`" + ` | '_ \/ __\ \ /\ / /_  / |_  / _` + "`" + ` | '__/ _` + "`" + ` |/ _` + "`" + ` |
| (_| | | | \__ \\ V  V / / /| |/ / (_| | | | (_| | (_| |
 \__,_|_| |_|___/ \_/\_/ /___|_/___\__,_|_|  \__,_|\__,_|`

func banner(w io.Writer) {
	fmt.Fprintln(w, bannerArt)
	fmt.Fprintf(w, "%56s\n\n", "v"+Version+" · a DNS proxy for your lab")
}

// NewRootCommand builds the command tree.
func NewRootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:   "dnswizard",
		Short: "A configurable DNS proxy for lab and development work",
		Long: strings.TrimSpace(`
dnswizard answers DNS queries from records you define and forwards everything
else to a real resolver, so you can point a name at your laptop without editing
hosts files on every machine.

Wildcard matching, hot config reload, and UDP and TCP served at the same time.`),
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       Version,
	}

	// AGPL section 5(d) asks that interactive interfaces carry the copyright,
	// the absence of warranty, and where to read the licence.
	root.SetVersionTemplate(strings.TrimSpace(`
dnswizard {{.Version}}
Copyright (C) 2026 Willem Mouton
License AGPL-3.0-only: GNU Affero GPL version 3 <https://gnu.org/licenses/agpl-3.0.html>
This is free software: you are free to change and redistribute it under its terms.
There is NO WARRANTY, to the extent permitted by law.
Commercial licences for use in a proprietary product or service: see COMMERCIAL.md
`) + "\n")
	root.AddCommand(newServeCommand(), newConfigCommand(), newQueryCommand())
	return root
}

// Execute runs the CLI and returns the process exit code.
func Execute() int {
	if err := NewRootCommand().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	return 0
}
